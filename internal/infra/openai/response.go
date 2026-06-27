package openai

import (
	"encoding/json"
	"net/http"
)

type Response struct {
	Error Info `json:"error"`
}

type Info struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    int     `json:"code"`
}

func WriteTooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(Response{
		Error: Info{
			Message: "Too many requests",
			Type:    "TooManyRequestsError",
			Param:   nil,
			Code:    http.StatusTooManyRequests,
		},
	})
}

func WriteUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(Response{
		Error: Info{
			Message: "Invalid or missing Authorization header",
			Type:    "AuthenticationError",
			Param:   nil,
			Code:    http.StatusUnauthorized,
		},
	})
}
