package server

import (
	"bytes"
	"errors"
	"net/http"
)

func mutationErrorStatus(err error) int {
	if errors.Is(err, errMutationRecoveryRequired) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, errInvalidPath) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

type mutationResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *mutationResponse) Header() http.Header { return r.header }

func (r *mutationResponse) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *mutationResponse) Write(contents []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(contents)
}

func (a *App) beginMutationResponse(w http.ResponseWriter, modelID string) (http.ResponseWriter, func(), error) {
	mutation, err := a.beginMutation(modelID)
	if err != nil {
		return w, nil, err
	}
	response := &mutationResponse{header: w.Header().Clone()}
	finish := func() {
		if interrupted := recover(); interrupted != nil {
			_ = mutation.finish(errors.New("mutation interrupted"))
			panic(interrupted)
		}
		var operationErr error
		if response.status >= 400 {
			operationErr = errors.New("mutation request failed")
		}
		if err := mutation.finish(operationErr); err != nil && (operationErr == nil || a.maintenance.Load()) {
			status := http.StatusInternalServerError
			if a.maintenance.Load() {
				status = http.StatusServiceUnavailable
			}
			writeError(w, status, err)
			return
		}
		for name, values := range response.header {
			w.Header()[name] = values
		}
		if response.status == 0 {
			response.status = http.StatusOK
		}
		w.WriteHeader(response.status)
		_, _ = w.Write(response.body.Bytes())
	}
	return response, finish, nil
}

func (m *dataMutation) finishOnReturn(operationErr *error) {
	if interrupted := recover(); interrupted != nil {
		_ = m.finish(errors.New("mutation interrupted"))
		panic(interrupted)
	}
	*operationErr = m.finish(*operationErr)
}
