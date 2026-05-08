package api

import (
	"encoding/json"
	"net/http"

	"github.com/rot13maxi/clankwork/internal/model"
)

func OK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(model.APIResponse{OK: true, Data: data})
}

func Fail(w http.ResponseWriter, code int, errCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(model.APIResponse{
		OK:    false,
		Error: &model.APIError{Code: errCode, Message: msg},
	})
}

func Decode(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}
