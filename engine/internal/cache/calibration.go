package cache

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// CalibrationStore persists human-labeled examples and their
// machine-judged counterparts so reports can show how closely the judge
// agrees with the gold labels for each (judge_provider, judge_model,
// rubric_name, rubric_version, prompt_hash) bucket. Agreement is a property
// of one judge model, so labels scored by different providers or models are
// never pooled. Backed by the same *sql.DB as HistoryStore so
// calibration data lives next to the assertion history that consumes it.
type CalibrationStore struct {
	db *sql.DB
}

// CalibrationLabel is one row of human-labeled training data plus the
// score the judge produced when run against the same input. Score and
// JudgedAt are populated by the calibration runner; HumanLabel and Input
// come from the loader.
type CalibrationLabel struct {
	JudgeProvider string
	JudgeModel    string
	RubricName    string
	RubricVersion string
	PromptHash    string
	Input         string
	HumanLabel    float64
	JudgeScore    float64
	JudgedAt      time.Time
}

// CalibrationKey selects the labels of one judge configuration. PromptHash
// is ignored by PairsForRubric.
type CalibrationKey struct {
	JudgeProvider string
	JudgeModel    string
	RubricName    string
	RubricVersion string
	PromptHash    string
}

// NewCalibrationStore creates the calibration_labels table and index if
// missing and migrates a table created before labels were keyed by judge
// provider and model. Migrated rows keep an empty provider and model: they
// are only returned for an empty key and never pooled with a provider's
// labels. Returns the wrapper for read/write access.
func NewCalibrationStore(db *sql.DB) (*CalibrationStore, error) {
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS calibration_labels (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			rubric_name     TEXT    NOT NULL,
			rubric_version  TEXT    NOT NULL,
			prompt_hash     TEXT    NOT NULL,
			judge_provider  TEXT    NOT NULL DEFAULT '',
			judge_model     TEXT    NOT NULL DEFAULT '',
			input           TEXT    NOT NULL,
			human_label     REAL    NOT NULL,
			judge_score     REAL    NOT NULL,
			judged_at       INTEGER NOT NULL
		)
	`); err != nil {
		return nil, fmt.Errorf("create calibration_labels: %w", err)
	}
	if err := migrateCalibrationJudgeKey(db); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		DROP INDEX IF EXISTS idx_calibration_key;
		CREATE INDEX IF NOT EXISTS idx_calibration_judge_key
		ON calibration_labels (judge_provider, judge_model, rubric_name, rubric_version, prompt_hash)
	`); err != nil {
		return nil, fmt.Errorf("create calibration index: %w", err)
	}
	return &CalibrationStore{db: db}, nil
}

// migrateCalibrationJudgeKey adds the judge_provider and judge_model columns
// to a calibration_labels table that predates them. Idempotent.
func migrateCalibrationJudgeKey(db *sql.DB) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('calibration_labels')`)
	if err != nil {
		return fmt.Errorf("inspect calibration_labels: %w", err)
	}
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scan calibration_labels column: %w", err)
		}
		present[name] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close calibration_labels columns: %w", err)
	}
	for _, col := range []string{"judge_provider", "judge_model"} {
		if present[col] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE calibration_labels ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add calibration_labels.%s: %w", col, err)
		}
	}
	return nil
}

// Record inserts one calibration label. Inputs are stored verbatim so the
// store doubles as a regression dataset; callers should keep them small.
func (s *CalibrationStore) Record(label CalibrationLabel) error {
	if label.JudgeProvider == "" || label.JudgeModel == "" {
		return errors.New("judge_provider and judge_model must be set")
	}
	if label.RubricName == "" || label.RubricVersion == "" {
		return errors.New("rubric_name and rubric_version must be set")
	}
	if label.HumanLabel < 0 || label.HumanLabel > 1 {
		return fmt.Errorf("human_label %f outside [0,1]", label.HumanLabel)
	}
	if label.JudgeScore < 0 || label.JudgeScore > 1 {
		return fmt.Errorf("judge_score %f outside [0,1]", label.JudgeScore)
	}
	if label.JudgedAt.IsZero() {
		label.JudgedAt = time.Now()
	}
	_, err := s.db.Exec(
		`INSERT INTO calibration_labels
		 (judge_provider, judge_model, rubric_name, rubric_version, prompt_hash, input, human_label, judge_score, judged_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		label.JudgeProvider, label.JudgeModel, label.RubricName, label.RubricVersion, label.PromptHash, label.Input,
		label.HumanLabel, label.JudgeScore, label.JudgedAt.UnixNano(),
	)
	if err != nil {
		return fmt.Errorf("insert calibration label: %w", err)
	}
	return nil
}

// Pairs returns the (human_label, judge_score) pairs stored under the full
// key. Empty slice when no rows match.
func (s *CalibrationStore) Pairs(key CalibrationKey) ([]LabelPair, error) {
	return s.scanPairs(
		`SELECT human_label, judge_score FROM calibration_labels
		 WHERE judge_provider = ? AND judge_model = ?
		   AND rubric_name = ? AND rubric_version = ? AND prompt_hash = ?
		 ORDER BY judged_at ASC`,
		key.JudgeProvider, key.JudgeModel, key.RubricName, key.RubricVersion, key.PromptHash,
	)
}

// PairsForRubric returns all (human, judge) pairs for the judge provider,
// model, rubric and version regardless of prompt_hash. Use when the caller
// wants an aggregate agreement number across the whole rubric.
func (s *CalibrationStore) PairsForRubric(key CalibrationKey) ([]LabelPair, error) {
	return s.scanPairs(
		`SELECT human_label, judge_score FROM calibration_labels
		 WHERE judge_provider = ? AND judge_model = ? AND rubric_name = ? AND rubric_version = ?
		 ORDER BY judged_at ASC`,
		key.JudgeProvider, key.JudgeModel, key.RubricName, key.RubricVersion,
	)
}

// scanPairs runs the given two-column (human, judge) query and accumulates
// the rows into LabelPair values. Wraps every error with enough context
// for the caller to identify which stage failed.
func (s *CalibrationStore) scanPairs(query string, args ...any) ([]LabelPair, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query calibration pairs: %w", err)
	}
	defer rows.Close()
	var out []LabelPair
	for rows.Next() {
		var p LabelPair
		if err := rows.Scan(&p.Human, &p.Judge); err != nil {
			return nil, fmt.Errorf("scan pair: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pairs: %w", err)
	}
	return out, nil
}

// LabelPair is one human/judge score pair. Both values are in [0,1].
type LabelPair struct {
	Human float64
	Judge float64
}
