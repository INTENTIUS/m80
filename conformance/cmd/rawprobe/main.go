package main

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/intentius/m80/conformance/runner"
)

func main() {
	method, url := os.Args[1], os.Args[2]
	req, _ := http.NewRequest(method, url, nil)
	if err := runner.Sign(req, nil, runner.CredentialsFromEnv(), "us-east-2"); err != nil {
		panic(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Println(resp.StatusCode)
	fmt.Println(string(body))
}
