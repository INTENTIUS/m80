package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// SigningName is shared by both MicroVM services ("Lambda Microvms" and
// "Lambda Core" sign as lambda — verified against the Smithy models, #2).
const SigningName = "lambda"

// Sign applies sigv4 to the request. Emulators do not verify signatures, but
// signing unconditionally keeps the recording path against real AWS identical
// to the emulator path.
func Sign(req *http.Request, payload []byte, creds aws.Credentials, region string) error {
	sum := sha256.Sum256(payload)
	return v4.NewSigner().SignHTTP(context.Background(), creds, req, hex.EncodeToString(sum[:]), SigningName, region, time.Now())
}

// CredentialsFromEnv returns the standard AWS env-var credentials when set
// (the recording path), and deterministic dummy credentials otherwise (the
// emulator path).
func CredentialsFromEnv() aws.Credentials {
	if id := os.Getenv("AWS_ACCESS_KEY_ID"); id != "" {
		return aws.Credentials{
			AccessKeyID:     id,
			SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
		}
	}
	return aws.Credentials{AccessKeyID: "m80", SecretAccessKey: "m80"}
}
