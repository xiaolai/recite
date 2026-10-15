// Package openai is recite's view of the shared OpenAI HTTP client and failure taxonomy
// (github.com/xiaolai/sayso/go/openaihttp): recite adds its exit codes, the verify-failed class,
// the route names it tracks route denials by, the per-class timeouts, the test guard on the
// real network, and the retrier.
package openai
