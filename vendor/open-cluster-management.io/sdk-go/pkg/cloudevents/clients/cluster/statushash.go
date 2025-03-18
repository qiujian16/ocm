package cluster

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	clusterv1 "open-cluster-management.io/api/cluster/v1"
)

// ManagedClusterStatusHash returns the SHA256 checksum of a ManagedCluster status.
func ManagedClusterStatusHash(cluster *clusterv1.ManagedCluster) (string, error) {
	statusBytes, err := json.Marshal(cluster.Status)
	if err != nil {
		return "", fmt.Errorf("failed to marshal cluster status, %v", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(statusBytes)), nil
}
