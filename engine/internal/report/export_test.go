package report

// CodeSpanForTest exposes codeSpan to the external test package.
func CodeSpanForTest(s string, escapePipes bool) string { return codeSpan(s, escapePipes) }
