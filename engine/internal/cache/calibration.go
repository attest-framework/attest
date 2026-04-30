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
// agrees with the gold labels for each (rubric_name, rubric_version,
// prompt_hash) bucket. Backed by the same *sql.DB as HistoryStore so
// calibration data lives next to the assertion history that consumes it.
type CalibrationStore struct {
	db *sql.DB
}

// CalibrationLabel is one row of human-labeled training data plus the
// score the judge produced when run against the same input. Score and
// JudgedAt are populated by the calibration runner; HumanLabel and Input
// come from the loader.
type CalibrationLabel struct {
	RubricName    string
	RubricVersion string
	PromptHash    string
	Input         string
	HumanLabel    float64
	JudgeScore    float64
	JudgedAt      time.Time
}

// NewCalibrationStore creates the calibration_labels table and index if
// missing. Returns the wrapper for read/write access.
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
			input           TEXT    NOT NULL,
			human_label     REAL    NOT NULL,
			judge_score     REAL    NOT NULL,
			judged_at       INTEGER NOT NULL
		)
	`); err != nil {
		return nil, fmt.Errorf("create calibration_labels: %w", err)
	}
	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_calibration_key
		ON calibration_labels (rubric_name, rubric_version, prompt_hash)
	`); err != nil {
		return nil, fmt.Errorf("create calibration index: %w", err)
	}
	return &CalibrationStore{db: db}, nil
}

// Record inserts one calibration label. Inputs are stored verbatim so the
// store doubles as a regression dataset; callers should keep them small.
func (s *CalibrationStore) Record(label CalibrationLabel) error {
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
		 (rubric_name, rubric_version, prompt_hash, input, human_label, judge_score, judged_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		label.RubricName, label.RubricVersion, label.PromptHash, label.Input,
		label.HumanLabel, label.JudgeScore, label.JudgedAt.UnixNano(),
	)
	if err != nil {
		return fmt.Errorf("insert calibration label: %w", err)
	}
	return nil
}

// Pairs returns the (human_label, judge_score) pairs stored under the given
// rubric+version+prompt_hash key. Empty slice when no rows match.
func (s *CalibrationStore) Pairs(rubricName, rubricVersion, promptHash string) ([]LabelPair, error) {
	rows, err := s.db.Query(
		`SELECT human_label, judge_score FROM calibration_labels
		 WHERE rubric_name = ? AND rubric_version = ? AND prompt_hash = ?
		 ORDER BY judged_at ASC`,
		rubricName, rubricVersion, promptHash,
	)
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

// PairsForRubric returns all (human, judge) pairs for the rubric+version
// regardless of prompt_hash. Use when the caller wants an aggregate
// agreement number across the whole rubric.
func (s *CalibrationStore) PairsForRubric(rubricName, rubricVersion string) ([]LabelPair, error) {
	rows, err := s.db.Query(
		`SELECT human_label, judge_score FROM calibration_labels
		 WHERE rubric_name = ? AND rubric_version = ?
		 ORDER BY judged_at ASC`,
		rubricName, rubricVersion,
	)
	if err != nil {
		return nil, fmt.Errorf("query rubric pairs: %w", err)
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
		return nil, fmt.Errorf("iterate rubric pairs: %w", err)
	}
	return out, nil
}

// LabelPair is one human/judge score pair. Both values are in [0,1].
type LabelPair struct {
	Human float64
	Judge float64
}
