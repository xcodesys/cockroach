// Copyright 2022 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package nstree

import (
	"context"
	"strings"

	"github.com/cockroachdb/cockroach/pkg/clusterversion"
	"github.com/cockroachdb/cockroach/pkg/keys"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/catalogkeys"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/descpb"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/internal/validate"
	"github.com/cockroachdb/errors"
)

// Catalog is used to store an in-memory copy of the whole catalog, or a portion
// thereof, as well as metadata like comment and zone configs.
type Catalog struct {
	byID     byIDMap
	byName   byNameMap
	byteSize int64
}

// ForEachDescriptorEntry iterates over all descriptor table entries in an
// ordered fashion.
func (c Catalog) ForEachDescriptorEntry(fn func(desc catalog.Descriptor) error) error {
	if !c.IsInitialized() {
		return nil
	}
	return c.byID.ascend(func(entry catalog.NameEntry) error {
		if d := entry.(*byIDEntry).desc; d != nil {
			return fn(d)
		}
		return nil
	})
}

// ForEachCommentEntry iterates through all descriptor comments in the same
// order as in system.comments.
func (c Catalog) ForEachCommentEntry(fn func(key catalogkeys.CommentKey, cmt string) error) error {
	if !c.IsInitialized() {
		return nil
	}
	return c.byID.ascend(func(entry catalog.NameEntry) error {
		return entry.(*byIDEntry).forEachComment(fn)
	})
}

// ForEachZoneConfigEntry iterates through all descriptor zone configs
// in order of increasing descriptor IDs.
func (c Catalog) ForEachZoneConfigEntry(
	fn func(id descpb.ID, zoneConfig catalog.ZoneConfig) error,
) error {
	if !c.IsInitialized() {
		return nil
	}
	return c.byID.ascend(func(entry catalog.NameEntry) error {
		if e := entry.(*byIDEntry); e.zc != nil {
			return fn(e.id, e.zc)
		}
		return nil
	})
}

// ForEachNamespaceEntry iterates over all name -> ID mappings in the same
// order as in system.namespace.
func (c Catalog) ForEachNamespaceEntry(fn func(e NamespaceEntry) error) error {
	if !c.IsInitialized() {
		return nil
	}
	return c.byName.ascend(func(entry catalog.NameEntry) error {
		return fn(entry.(NamespaceEntry))
	})
}

// ForEachSchemaNamespaceEntryInDatabase iterates over all namespace
// entries in an ordered fashion for the entries corresponding to
// schemas in the requested database.
func (c Catalog) ForEachSchemaNamespaceEntryInDatabase(
	dbID descpb.ID, fn func(e NamespaceEntry) error,
) error {
	if !c.IsInitialized() {
		return nil
	}
	return c.byName.ascendSchemasForDatabase(dbID, func(entry catalog.NameEntry) error {
		return fn(entry.(NamespaceEntry))
	})
}

// LookupDescriptorEntry looks up a descriptor by ID.
func (c Catalog) LookupDescriptorEntry(id descpb.ID) catalog.Descriptor {
	if !c.IsInitialized() || id == descpb.InvalidID {
		return nil
	}
	e := c.byID.get(id)
	if e == nil {
		return nil
	}
	return e.(*byIDEntry).desc
}

// LookupNamespaceEntry looks up a descriptor ID by name.
func (c Catalog) LookupNamespaceEntry(key catalog.NameKey) NamespaceEntry {
	if !c.IsInitialized() || key == nil {
		return nil
	}
	e := c.byName.getByName(key.GetParentID(), key.GetParentSchemaID(), key.GetName())
	if e == nil {
		return nil
	}
	return e.(NamespaceEntry)
}

// OrderedDescriptors returns the descriptors in an ordered fashion.
func (c Catalog) OrderedDescriptors() []catalog.Descriptor {
	if !c.IsInitialized() {
		return nil
	}
	ret := make([]catalog.Descriptor, 0, c.byID.t.Len())
	_ = c.ForEachDescriptorEntry(func(desc catalog.Descriptor) error {
		ret = append(ret, desc)
		return nil
	})
	return ret
}

// OrderedDescriptorIDs returns the descriptor IDs in an ordered fashion.
func (c Catalog) OrderedDescriptorIDs() []descpb.ID {
	if !c.IsInitialized() {
		return nil
	}
	ret := make([]descpb.ID, 0, c.byName.t.Len())
	_ = c.ForEachNamespaceEntry(func(e NamespaceEntry) error {
		ret = append(ret, e.GetID())
		return nil
	})
	return ret
}

// IsInitialized returns false if the underlying map has not yet been
// initialized. Initialization is done lazily when
func (c Catalog) IsInitialized() bool {
	return c.byID.initialized() && c.byName.initialized()
}

var _ validate.ValidationDereferencer = Catalog{}

// DereferenceDescriptors implements the validate.ValidationDereferencer
// interface.
func (c Catalog) DereferenceDescriptors(
	ctx context.Context, version clusterversion.ClusterVersion, reqs []descpb.ID,
) ([]catalog.Descriptor, error) {
	ret := make([]catalog.Descriptor, len(reqs))
	for i, id := range reqs {
		ret[i] = c.LookupDescriptorEntry(id)
	}
	return ret, nil
}

// DereferenceDescriptorIDs implements the validate.ValidationDereferencer
// interface.
func (c Catalog) DereferenceDescriptorIDs(
	_ context.Context, reqs []descpb.NameInfo,
) ([]descpb.ID, error) {
	ret := make([]descpb.ID, len(reqs))
	for i, req := range reqs {
		ne := c.LookupNamespaceEntry(req)
		if ne == nil {
			continue
		}
		ret[i] = ne.GetID()
	}
	return ret, nil
}

// Validate delegates to validate.Validate.
func (c Catalog) Validate(
	ctx context.Context,
	version clusterversion.ClusterVersion,
	telemetry catalog.ValidationTelemetry,
	targetLevel catalog.ValidationLevel,
	descriptors ...catalog.Descriptor,
) (ve catalog.ValidationErrors) {
	return validate.Validate(ctx, version, c, telemetry, targetLevel, descriptors...)
}

// ValidateNamespaceEntry returns an error if the specified namespace entry
// is invalid.
func (c Catalog) ValidateNamespaceEntry(key catalog.NameKey) error {
	ne := c.LookupNamespaceEntry(key)
	if ne == nil {
		return errors.New("invalid descriptor ID")
	}
	// Handle special cases.
	switch ne.GetID() {
	case descpb.InvalidID:
		return errors.New("invalid descriptor ID")
	case keys.PublicSchemaID:
		// The public schema doesn't have a descriptor.
		return nil
	default:
		isSchema := ne.GetParentID() != keys.RootNamespaceID && ne.GetParentSchemaID() == keys.RootNamespaceID
		if isSchema && strings.HasPrefix(ne.GetName(), "pg_temp_") {
			// Temporary schemas have namespace entries but not descriptors.
			return nil
		}
	}
	// Compare the namespace entry with the referenced descriptor.
	desc := c.LookupDescriptorEntry(ne.GetID())
	if desc == nil {
		return catalog.ErrDescriptorNotFound
	}
	if desc.Dropped() {
		return errors.Newf("no matching name info in draining names of dropped %s",
			desc.DescriptorType())
	}
	if ne.GetParentID() == desc.GetParentID() &&
		ne.GetParentSchemaID() == desc.GetParentSchemaID() &&
		ne.GetName() == desc.GetName() {
		return nil
	}
	return errors.Newf("no matching name info found in non-dropped %s %q",
		desc.DescriptorType(), desc.GetName())
}

// ValidateWithRecover is like Validate but which recovers from panics.
// This is useful when we're validating many descriptors separately and we don't
// want a corrupt descriptor to prevent validating the others.
func (c Catalog) ValidateWithRecover(
	ctx context.Context, version clusterversion.ClusterVersion, desc catalog.Descriptor,
) (ve catalog.ValidationErrors) {
	defer func() {
		if r := recover(); r != nil {
			err, ok := r.(error)
			if !ok {
				err = errors.Newf("%v", r)
			}
			err = errors.WithAssertionFailure(errors.Wrap(err, "validation"))
			ve = append(ve, err)
		}
	}()
	return c.Validate(ctx, version, catalog.NoValidationTelemetry, validate.Write, desc)
}

// ByteSize returns memory usage of the underlying map in bytes.
func (c Catalog) ByteSize() int64 {
	return c.byteSize
}
