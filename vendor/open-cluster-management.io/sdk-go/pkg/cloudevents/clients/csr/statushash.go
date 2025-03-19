package csr

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	certificatev1 "k8s.io/api/certificates/v1"
)

// CSRStatusHash returns the SHA256 checksum of a CSR status.
func CSRStatusHash(csr *certificatev1.CertificateSigningRequest) (string, error) {
	statusBytes, err := json.Marshal(csr.Status)
	if err != nil {
		return "", fmt.Errorf("failed to marshal csr status, %v", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(statusBytes)), nil
}
