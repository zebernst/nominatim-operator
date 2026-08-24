package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// VolumeClaimTemplate.metadata must not embed metav1.ObjectMeta: that type always
// JSON-marshals creationTimestamp:null, which the CRD rejects as an unknown field
// (KubeAPIWarningLogger on spec.*.volumeClaimTemplate.metadata.creationTimestamp
// and spec.database.cluster.storage.metadata.creationTimestamp).
func TestVolumeClaimTemplateMetadataJSONOmitsServerFields(t *testing.T) {
	t.Parallel()

	size := resource.MustParse("10Gi")
	vct := VolumeClaimTemplate{
		Metadata: EmbeddedObjectMeta{
			Name:        "data",
			Labels:      map[string]string{"app": "nominatim"},
			Annotations: map[string]string{"note": "x"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: size},
			},
		},
	}

	raw, err := json.Marshal(vct)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	for _, bad := range []string{
		"creationTimestamp",
		"resourceVersion",
		"uid",
		"generation",
		"managedFields",
	} {
		if strings.Contains(s, bad) {
			t.Fatalf("VolumeClaimTemplate JSON must not contain %q; got %s", bad, s)
		}
	}
	if !strings.Contains(s, `"name":"data"`) {
		t.Fatalf("expected name in JSON, got %s", s)
	}
	if !strings.Contains(s, `"labels"`) || !strings.Contains(s, `"annotations"`) {
		t.Fatalf("expected labels/annotations in JSON, got %s", s)
	}
}
