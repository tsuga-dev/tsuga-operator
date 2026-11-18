package tsugamapper

import (
	"encoding/json"
	"testing"

	"k8s.io/utils/ptr"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

type folderMapper func(name, owner, ownerName string, parentID *string, parentRef *v1alpha1.LocalObjectReference) ([]byte, error)

func folderMappers() map[string]folderMapper {
	return map[string]folderMapper{
		"monitor folder": func(name, owner, ownerName string, parentID *string, parentRef *v1alpha1.LocalObjectReference) ([]byte, error) {
			return MonitorFolderSpecToPayload(v1alpha1.MonitorFolderSpec{
				Name: name, Owner: owner, OwnerName: ownerName, ParentFolderID: parentID, ParentFolderRef: parentRef,
			})
		},
		"dashboard folder": func(name, owner, ownerName string, parentID *string, parentRef *v1alpha1.LocalObjectReference) ([]byte, error) {
			return DashboardFolderSpecToPayload(v1alpha1.DashboardFolderSpec{
				Name: name, Owner: owner, OwnerName: ownerName, ParentFolderID: parentID, ParentFolderRef: parentRef,
			})
		},
	}
}

func decodeFolder(t *testing.T, mapper folderMapper, parentID *string, parentRef *v1alpha1.LocalObjectReference, ownerName string) map[string]any {
	t.Helper()
	payload, err := mapper("Example", "team-1", ownerName, parentID, parentRef)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestFolderSpecToPayload_SendsParentFolderID(t *testing.T) {
	for name, mapper := range folderMappers() {
		t.Run(name, func(t *testing.T) {
			body := decodeFolder(t, mapper, ptr.To("p1"), nil, "")
			if body["parentFolderId"] != "p1" {
				t.Errorf("expected parentFolderId p1, got %v", body["parentFolderId"])
			}
		})
	}
}

// The Tsuga API keeps a folder's current parent when parentFolderId is absent
// on update and moves it to top level on null.
func TestFolderSpecToPayload_OmitsUnsetParent(t *testing.T) {
	for name, mapper := range folderMappers() {
		t.Run(name, func(t *testing.T) {
			body := decodeFolder(t, mapper, nil, nil, "")
			if _, ok := body["parentFolderId"]; ok {
				t.Errorf("expected parentFolderId omitted, got %v", body["parentFolderId"])
			}
		})
	}
}

func TestFolderSpecToPayload_DropsKubernetesOnlyFields(t *testing.T) {
	for name, mapper := range folderMappers() {
		t.Run(name, func(t *testing.T) {
			body := decodeFolder(t, mapper, nil, &v1alpha1.LocalObjectReference{Name: "parent"}, "checkout")
			for _, key := range []string{"ownerName", "parentFolderRef"} {
				if _, ok := body[key]; ok {
					t.Errorf("expected %s omitted, got %v", key, body[key])
				}
			}
		})
	}
}
