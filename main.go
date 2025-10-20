/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// FormatHeader name of the header used to extract the format
	FormatHeader = "X-Format"

	// CodeHeader name of the header used as source of the HTTP status code to return
	CodeHeader = "X-Code"

	// ContentType name of the header that defines the format of the reply
	ContentType = "Content-Type"

	// OriginalURI name of the header with the original URL from NGINX
	OriginalURI = "X-Original-URI"

	// Namespace name of the header that contains information about the Ingress namespace
	Namespace = "X-Namespace"

	// IngressName name of the header that contains the matched Ingress
	IngressName = "X-Ingress-Name"

	// ServiceName name of the header that contains the matched Service in the Ingress
	ServiceName = "X-Service-Name"

	// ServicePort name of the header that contains the matched Service port in the Ingress
	ServicePort = "X-Service-Port"

	// RequestId is a unique ID that identifies the request - same as for backend service
	RequestId = "X-Request-ID"

	// ErrFilesPathVar is the name of the environment variable indicating
	// the location on disk of files served by the handler.
	ErrFilesPathVar = "ERROR_FILES_PATH"

	// DefaultFormatVar is the name of the environment variable indicating
	// the default error MIME type that should be returned if either the
	// client does not specify an Accept header, or the Accept header provided
	// cannot be mapped to a file extension.
	DefaultFormatVar = "DEFAULT_RESPONSE_FORMAT"
)

func init() {
	prometheus.MustRegister(requestCount)
	prometheus.MustRegister(requestDuration)
}

func main() {
	// Configure logger to output raw JSON without timestamp prefix for Datadog
	log.SetFlags(0)
	
	errFilesPath := "/www"
	if os.Getenv(ErrFilesPathVar) != "" {
		errFilesPath = os.Getenv(ErrFilesPathVar)
	}

	defaultFormat := "text/html"
	if os.Getenv(DefaultFormatVar) != "" {
		defaultFormat = os.Getenv(DefaultFormatVar)
	}

	http.HandleFunc("/", errorHandler(errFilesPath, defaultFormat))

	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	http.ListenAndServe(fmt.Sprintf(":8080"), nil)
}

func errorHandler(path, defaultFormat string) func(http.ResponseWriter, *http.Request) {
	defaultExts, err := mime.ExtensionsByType(defaultFormat)
	if err != nil || len(defaultExts) == 0 {
		panic("couldn't get file extension for default format")
	}
	defaultExt := defaultExts[0]

	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ext := defaultExt
		var logJSON []byte // Declare logJSON variable for reuse throughout function

		// Log request details in JSON format with debug level
		requestLog := map[string]interface{}{
			"timestamp":    start.Format(time.RFC3339),
			"level":        "debug", // Set log level to debug for Datadog categorization
			"method":       r.Method,
			"path":         r.URL.Path,
			"query":        r.URL.RawQuery,
			"host":         r.Host,
			"remote_addr":  r.RemoteAddr,
			"user_agent":   r.Header.Get("User-Agent"),
			"original_uri": r.Header.Get(OriginalURI),
			"namespace":    r.Header.Get(Namespace),
			"ingress_name": r.Header.Get(IngressName),
			"service_name": r.Header.Get(ServiceName),
			"service_port": r.Header.Get(ServicePort),
			"request_id":   r.Header.Get(RequestId),
			"x_code":       r.Header.Get(CodeHeader),
			"x_format":     r.Header.Get(FormatHeader),
			"accept":       r.Header.Get("Accept"),
		}

		if os.Getenv("DEBUG") != "" {
			w.Header().Set(FormatHeader, r.Header.Get(FormatHeader))
			w.Header().Set(CodeHeader, r.Header.Get(CodeHeader))
			w.Header().Set(ContentType, r.Header.Get(ContentType))
			w.Header().Set(OriginalURI, r.Header.Get(OriginalURI))
			w.Header().Set(Namespace, r.Header.Get(Namespace))
			w.Header().Set(IngressName, r.Header.Get(IngressName))
			w.Header().Set(ServiceName, r.Header.Get(ServiceName))
			w.Header().Set(ServicePort, r.Header.Get(ServicePort))
			w.Header().Set(RequestId, r.Header.Get(RequestId))
		}

		format := r.Header.Get(FormatHeader)
		if format == "" {
			format = defaultFormat
			if os.Getenv("DEBUG") != "" {
				// Log debug message in JSON format for Datadog
				debugLog := map[string]interface{}{
					"timestamp": time.Now().Format(time.RFC3339),
					"level":     "debug",
					"message":   fmt.Sprintf("format not specified. Using %v", format),
					"context":   "format_selection",
				}
				logJSON, _ = json.Marshal(debugLog)
				log.Printf("%s", logJSON)
			}
		}

		cext, err := mime.ExtensionsByType(format)
		if err != nil {
			// Log error message in JSON format for Datadog
			errorLog := map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
				"level":     "debug",
				"message":   fmt.Sprintf("unexpected error reading media type extension: %v. Using %v", err, ext),
				"context":   "mime_type_parsing",
				"error":     err.Error(),
			}
			logJSON, _ = json.Marshal(errorLog)
			log.Printf("%s", logJSON)
			format = defaultFormat
		} else if len(cext) == 0 {
			// Log warning message in JSON format for Datadog
			warnLog := map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
				"level":     "debug",
				"message":   fmt.Sprintf("couldn't get media type extension. Using %v", ext),
				"context":   "mime_extension_lookup",
			}
			logJSON, _ = json.Marshal(warnLog)
			log.Printf("%s", logJSON)
		} else {
			ext = cext[0]
		}
		w.Header().Set(ContentType, format)

		errCode := r.Header.Get(CodeHeader)
		code := 404 // Default code
		if errCode != "" {
			parsedCode, err := strconv.Atoi(errCode)
			if err != nil {
				// Log error message in JSON format for Datadog
				errorLog := map[string]interface{}{
					"timestamp": time.Now().Format(time.RFC3339),
					"level":     "debug",
					"message":   fmt.Sprintf("invalid return code %q: %v. Using %v", errCode, err, code),
					"context":   "return_code_parsing",
					"error":     err.Error(),
				}
				logJSON, _ = json.Marshal(errorLog)
				log.Printf("%s", logJSON)
			} else {
				code = parsedCode
			}
		} else if os.Getenv("DEBUG") != "" {
			// Log debug message in JSON format for Datadog
			debugLog := map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
				"level":     "debug",
				"message":   fmt.Sprintf("return code not specified. Using %v", code),
				"context":   "return_code_default",
			}
			logJSON, _ = json.Marshal(debugLog)
			log.Printf("%s", logJSON)
		}
		w.WriteHeader(code)

		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		// special case for compatibility
		if ext == ".htm" {
			ext = ".html"
		}
		file := fmt.Sprintf("%v/%v%v", path, code, ext)
		f, err := os.Open(file)
		if err != nil {
			// Log error message in JSON format for Datadog
			errorLog := map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
				"level":     "debug",
				"message":   fmt.Sprintf("unexpected error opening file: %v", err),
				"context":   "file_opening",
				"error":     err.Error(),
			}
			logJSON, _ = json.Marshal(errorLog)
			log.Printf("%s", logJSON)
			scode := strconv.Itoa(code)
			file := fmt.Sprintf("%v/%cxx%v", path, scode[0], ext)
			f, err := os.Open(file)
			if err != nil {
				// Log error message in JSON format for Datadog
				errorLog := map[string]interface{}{
					"timestamp": time.Now().Format(time.RFC3339),
					"level":     "debug",
					"message":   fmt.Sprintf("unexpected error opening file: %v", err),
					"context":   "file_opening",
					"error":     err.Error(),
				}
				logJSON, _ = json.Marshal(errorLog)
				log.Printf("%s", logJSON)
				http.NotFound(w, r)
				return
			}
			defer f.Close()
			// Log info message in JSON format for Datadog
			infoLog := map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
				"level":     "debug",
				"message":   fmt.Sprintf("serving custom error response for code %v and format %v from file %v", code, format, file),
				"context":   "custom_error_response",
			}
			logJSON, _ = json.Marshal(infoLog)
			log.Printf("%s", logJSON)
			io.Copy(w, f)
			return
		}
		defer f.Close()
		io.Copy(w, f)

		duration := time.Now().Sub(start).Seconds()

		proto := strconv.Itoa(r.ProtoMajor)
		proto = fmt.Sprintf("%s.%s", proto, strconv.Itoa(r.ProtoMinor))

		requestCount.WithLabelValues(proto).Inc()
		requestDuration.WithLabelValues(proto).Observe(duration)

		// Add response details to log
		requestLog["status_code"] = code
		requestLog["content_type"] = format
		requestLog["response_file"] = file
		requestLog["duration_seconds"] = duration
		requestLog["protocol"] = proto
		requestLog["message"] = fmt.Sprintf("HTTP %s %s - %d", r.Method, r.URL.Path, code)
		requestLog["context"] = "http_request"

		// Output JSON log with debug level for Datadog
		logJSON, _ = json.Marshal(requestLog)
		log.Printf("%s", logJSON)
	}
}
