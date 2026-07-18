package chunking

// CountTokens returns an approximation of the token count for text.
// Uses the heuristic: tokens ≈ len(text) / 4.
func CountTokens(text string) int {
	return len(text) / 4
}
