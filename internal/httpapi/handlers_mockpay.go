package httpapi

import (
	"fmt"
	"net/http"

	"avitotask/internal/apperr"

	"github.com/google/uuid"
)

func (s *Server) MockPayPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("paymentToken")
	if _, err := uuid.Parse(token); err != nil {
		writeError(w, apperr.BadRequest("invalid payment token"))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `
<html>
<head><title>Mock payment</title></head>
<body>
<h1>Mock payment</h1>
<form method="post" action="/mock/pay/%s/pay">
<button type="submit">Pay</button>
</form>
</body>
</html>
`, token)
}

func (s *Server) MockPay(w http.ResponseWriter, r *http.Request) {
	token, err := uuid.Parse(r.PathValue("paymentToken"))
	if err != nil {
		writeError(w, apperr.BadRequest("invalid payment token"))
		return
	}

	if err := s.Orders.Pay(r.Context(), token); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, statusResponse{Status: "paid"})
}
