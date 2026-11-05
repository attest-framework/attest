package policy

import "fmt"

// MissingInputs lists the inputs the policy's configured rules need that the
// caller did not supply. A rule that compares against a baseline or a mutation
// report cannot run without it, so `policy evaluate` exits ExitUsage instead
// of passing the rule silently. It returns nil when every input is present.
func MissingInputs(p *Policy, haveBaseline, haveMutation bool) []string {
	if p == nil {
		return nil
	}
	var missing []string
	if !haveBaseline {
		if p.BlockOnRegression != nil && *p.BlockOnRegression {
			missing = append(missing, "--baseline <tag> (block_on_regression compares the report to a baseline)")
		}
		if p.Regression != nil {
			missing = append(missing, "--baseline <tag> (the regression rule compares trials to a baseline)")
		}
	}
	if !haveMutation && p.MinMutationScore != nil {
		missing = append(missing, "--mutation <path> (min_mutation_score needs the output of `attest mutate --json`)")
	}
	return missing
}

// MissingInputsError formats the result of MissingInputs for the CLI. It
// returns nil when nothing is missing.
func MissingInputsError(p *Policy, haveBaseline, haveMutation bool) error {
	missing := MissingInputs(p, haveBaseline, haveMutation)
	if len(missing) == 0 {
		return nil
	}
	msg := "the policy has rules that need input that was not given:"
	for _, m := range missing {
		msg += "\n  " + m
	}
	return fmt.Errorf("%s", msg)
}
