// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strings"

	"google.golang.org/protobuf/proto"
)

const (
	contentTypeProtobuf = "application/x-protobuf"
	maxProtobufBodySize = 64 << 20 // 64MiB
)

func wantsGzip(r *http.Request) bool {
	// Typical value: "gzip" or "gzip, deflate, br"
	return strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip")
}

func isProtobufContentType(ct string) bool {
	if ct == "" {
		return false
	}
	ct = strings.ToLower(ct)
	return strings.Contains(ct, contentTypeProtobuf)
}

func decodeProtobuf(r *http.Request, msg proto.Message) error {
	if !isProtobufContentType(r.Header.Get("Content-Type")) {
		return errors.New("invalid content-type: expected application/x-protobuf")
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, maxProtobufBodySize))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return errors.New("empty request body")
	}
	return proto.Unmarshal(data, msg)
}

func writeProtobuf(w http.ResponseWriter, r *http.Request, httpStatus int, msg proto.Message) {
	data, err := proto.Marshal(msg)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentTypeProtobuf)

	if wantsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		w.WriteHeader(httpStatus)
		gz := gzip.NewWriter(w)
		defer func() { _ = gz.Close() }()
		_, _ = gz.Write(data)
		return
	}

	w.WriteHeader(httpStatus)
	_, _ = w.Write(data)
}
