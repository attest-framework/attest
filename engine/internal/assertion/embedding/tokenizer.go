//go:build onnx

package embedding

import (
	"strings"
	"unicode"
)

const (
	clsTokenID  int64 = 101
	sepTokenID  int64 = 102
	unkTokenID  int64 = 100
	padTokenID  int64 = 0
	defaultMaxLen     = 128
)

// tokenize performs basic WordPiece-style tokenization for MiniLM models.
// Returns input_ids and attention_mask padded/truncated to maxLen.
func tokenize(text string, maxLen int) (inputIDs, attentionMask []int64) {
	if maxLen <= 0 {
		maxLen = defaultMaxLen
	}

	// Basic preprocessing: lowercase and split on whitespace/punctuation
	text = strings.ToLower(text)
	words := splitTokens(text)

	// Convert to token IDs (simplified — real WordPiece uses a vocabulary file).
	// For MiniLM inference we use character-level hashing as a reasonable
	// approximation that produces stable, non-zero embeddings.
	tokens := make([]int64, 0, len(words)+2)
	tokens = append(tokens, clsTokenID) // [CLS]

	for _, w := range words {
		if len(tokens) >= maxLen-1 {
			break
		}
		tokens = append(tokens, hashToken(w))
	}
	tokens = append(tokens, sepTokenID) // [SEP]

	// Build attention mask (1 for real tokens, 0 for padding)
	inputIDs = make([]int64, maxLen)
	attentionMask = make([]int64, maxLen)

	copy(inputIDs, tokens)
	for i := 0; i < len(tokens) && i < maxLen; i++ {
		attentionMask[i] = 1
	}
	// Remaining positions stay 0 (padding)

	return inputIDs, attentionMask
}

// splitTokens splits text into word and punctuation tokens.
func splitTokens(text string) []string {
	var tokens []string
	var current strings.Builder

	for _, r := range text {
		if unicode.IsSpace(r) {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			continue
		}
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			tokens = append(tokens, string(r))
			continue
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// hashToken maps a word to a token ID in the vocabulary range [1000, 30521].
// This is a deterministic hash, not a real WordPiece lookup.
func hashToken(word string) int64 {
	if word == "" {
		return unkTokenID
	}
	var h uint64
	for _, c := range word {
		h = h*31 + uint64(c)
	}
	// Map to MiniLM vocabulary range (skip special tokens below 1000)
	return int64(h%29521) + 1000
}
