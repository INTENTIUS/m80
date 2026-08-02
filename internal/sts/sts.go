// Package sts answers exactly one call, and exists for exactly one reason.
//
// The KubeMicroVM operator runs `sts:GetCallerIdentity` at boot, in
// AwsConnectivityStartup, before it will report ready. That client is built
// with a region and no endpoint override, so AWS_MICROVM_ENDPOINT does not
// reach it; without a reachable STS the health check reports
// awsConnectivity: false forever, readiness never passes, the admission
// webhook gets no endpoints, and every MicroVM CR create fails with
// "no endpoints available". The operator cannot start against an emulator at
// all. Filed upstream as codriverlabs/KubeMicroVM#50.
//
// Until that lands, something has to answer. A full AWS emulator will, and
// the UAT harness used one: 556 MiB of image and 190 MiB of resident memory
// to return a 400-byte XML document, next to m80's 8 MiB. That is a bad
// trade for anyone who wants to clone this and have it work.
//
// So: not an STS emulation. A startup-gate shim, off unless -serve-sts asks
// for it, answering GetCallerIdentity and refusing every other action with a
// 501 that says so. If it ever grows a second action, that decision should
// be argued rather than drifted into.
package sts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/intentius/m80/internal/images"
)

// Action is the one thing this package answers.
const Action = "GetCallerIdentity"

// apiVersion is the query-protocol version STS has used since 2011. The
// response namespace carries it, and clients match on it.
const apiVersion = "2011-06-15"

// identity is what a caller gets. The account is m80's own, so an ARN minted
// here matches the ARNs every other m80 response carries — the operator
// reads the account out of this call and compares.
var (
	account = images.AccountID
	arn     = "arn:aws:iam::" + images.AccountID + ":root"
)

// Handler answers STS query-protocol requests.
//
// STS is AWS Query, not rest-json like the rest of m80: the action arrives
// form-encoded in the body of a POST to /, and the response is XML. That is
// a second wire protocol inside one binary, which is worth noticing and is
// the main argument against doing this at all.
type Handler struct {
	// RequestID is the id echoed in ResponseMetadata. Injectable so a test
	// can assert the whole document rather than everything except one field.
	RequestID func() string
}

// Claims reports whether a request is an STS query call this should answer,
// so the server can consult it before its rest-json route table without
// swallowing anything else posted to /.
func Claims(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.Path != "/" {
		return false
	}
	ct := r.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "application/x-www-form-urlencoded")
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.errorResponse(w, http.StatusBadRequest, "Sender", "MalformedInput",
			"Could not parse the request body as a query string")
		return
	}

	action := r.PostForm.Get("Action")
	if action != Action {
		// 501 rather than an STS-shaped error, because this is not STS being
		// unable to do something. It is m80 declining to pretend to be STS,
		// and a caller reading the message should come away knowing that.
		h.errorResponse(w, http.StatusNotImplemented, "Sender", "InvalidAction",
			"m80 answers only sts:"+Action+", as a shim for a consumer's startup "+
				"connectivity check. It is not an STS emulator and action '"+
				action+"' will not be added by guessing. Point AWS_ENDPOINT_URL_STS "+
				"at a real STS emulator if you need one.")
		return
	}

	body, err := xml.Marshal(callerIdentityResponse{
		XMLName:   xml.Name{Local: "GetCallerIdentityResponse"},
		Namespace: "https://sts.amazonaws.com/doc/" + apiVersion + "/",
		Result: callerIdentityResult{
			// UserId is the account for a root caller, which is what m80
			// presents. A real IAM user or assumed role would carry a
			// distinct principal id; m80 evaluates no IAM, so root is the
			// only honest answer.
			UserID:  account,
			Account: account,
			Arn:     arn,
		},
		Metadata: responseMetadata{RequestID: h.requestID()},
	})
	if err != nil {
		h.errorResponse(w, http.StatusInternalServerError, "Receiver", "InternalFailure", err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(body)
}

func (h *Handler) requestID() string {
	if h.RequestID != nil {
		return h.RequestID()
	}
	return newUUID()
}

type callerIdentityResponse struct {
	XMLName   xml.Name
	Namespace string               `xml:"xmlns,attr"`
	Result    callerIdentityResult `xml:"GetCallerIdentityResult"`
	Metadata  responseMetadata     `xml:"ResponseMetadata"`
}

type callerIdentityResult struct {
	UserID  string `xml:"UserId"`
	Account string `xml:"Account"`
	Arn     string `xml:"Arn"`
}

type responseMetadata struct {
	RequestID string `xml:"RequestId"`
}

type errorResponse struct {
	XMLName   xml.Name  `xml:"ErrorResponse"`
	Namespace string    `xml:"xmlns,attr"`
	Error     errorBody `xml:"Error"`
	RequestID string    `xml:"RequestId"`
}

type errorBody struct {
	Type    string `xml:"Type"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func (h *Handler) errorResponse(w http.ResponseWriter, status int, kind, code, message string) {
	body, err := xml.Marshal(errorResponse{
		Namespace: "https://sts.amazonaws.com/doc/" + apiVersion + "/",
		Error:     errorBody{Type: kind, Code: code, Message: message},
		RequestID: h.requestID(),
	})
	if err != nil {
		http.Error(w, message, status)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(body)
}

// newUUID mints the request id. STS request ids are UUIDs and clients log
// them, so a well-formed one costs nothing and a malformed one would be
// noticed.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sts: no entropy: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
