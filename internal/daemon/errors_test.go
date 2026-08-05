package daemon

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go"
)

// oaiErrWithBody builds an *openai.Error whose SDK Message is empty (as happens
// with the ChatGPT/Codex backend, whose error body isn't wrapped in the
// {"error":{...}} envelope the SDK unwraps) but whose Response still carries the
// original body bytes.
func oaiErrWithBody(message, body string) *openai.Error {
	e := &openai.Error{Message: message, StatusCode: 400}
	if body != "" {
		e.Response = &http.Response{
			StatusCode: 400,
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}
	return e
}

func TestOpenAIErrorDetail(t *testing.T) {
	cases := []struct {
		name    string
		message string
		body    string
		want    string
	}{
		{
			name:    "sdk message wins",
			message: "model not allowed",
			body:    `{"detail":"ignored"}`,
			want:    "model not allowed",
		},
		{
			name: "codex detail field",
			body: `{"detail":"This model is not supported with your current plan."}`,
			want: "This model is not supported with your current plan.",
		},
		{
			name: "standard error envelope",
			body: `{"error":{"message":"Unsupported parameter: 'store'."}}`,
			want: "Unsupported parameter: 'store'.",
		},
		{
			name: "raw fallback for unknown shape",
			body: `something went wrong`,
			want: "something went wrong",
		},
		{
			name: "empty when no message and no body",
			body: "",
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := openAIErrorDetail(oaiErrWithBody(c.message, c.body))
			if got != c.want {
				t.Errorf("openAIErrorDetail() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestClassifyError_CodexBadRequestSurfacesDetail verifies a Codex-style 400
// (empty SDK Message, {"detail":...} body) yields an actionable friendly
// message rather than a bare "Bad request".
func TestClassifyError_CodexBadRequestSurfacesDetail(t *testing.T) {
	err := oaiErrWithBody("", `{"detail":"Your ChatGPT plan does not include this model."}`)
	retryable, msg := classifyError(err)
	if retryable {
		t.Errorf("400 should not be retryable")
	}
	want := "Bad request: Your ChatGPT plan does not include this model."
	if msg != want {
		t.Errorf("classifyError msg = %q, want %q", msg, want)
	}
}
