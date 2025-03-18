package common

import (
	certificatev1 "k8s.io/api/certificates/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
)

const (
	// CloudEventsResourceVersionAnnotationKey is the key of the managedcluster resourceversion annotation.
	//
	// This annotation is used for tracing the managedcluster specific changes, the value of this annotation
	// should be a sequence number representing the managedcluster specific generation.
	CloudEventsResourceVersionAnnotationKey = "cloudevents.open-cluster-management.io/resourceversion"
)

// CloudEventsOriginalSourceLabelKey is the key of the cloudevents original source label.
const CloudEventsOriginalSourceLabelKey = "cloudevents.open-cluster-management.io/originalsource"

// ConditionDeleted represents the object is deleted.
const ConditionDeleted = "Deleted"

const (
	CreateRequestAction = "create_request"
	UpdateRequestAction = "update_request"
	DeleteRequestAction = "delete_request"
)

var ManagedClusterGK = schema.GroupKind{Group: clusterv1.GroupName, Kind: "ManagedCluster"}
var ManagedClusterGR = schema.GroupResource{Group: clusterv1.GroupName, Resource: "managedclusters"}

var CSRGK = schema.GroupKind{Group: certificatev1.GroupName, Kind: "CertificateSigningRequest"}
var CSRGR = schema.GroupResource{Group: certificatev1.GroupName, Resource: "certificatesigningrequests"}
