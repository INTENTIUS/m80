package images

// The service returns four different projections of an image and two of a
// version, and the model describes none of them. Each function below is the
// shape one recorded fixture pins down; the differences between them are
// real, not tidying.

// detail is what Create and Update return: the whole resource, with every
// unset member present and null. A sparse body is the single biggest source
// of divergence for an emulator, because clients read members the model says
// are optional and the service always sends.
func detail(img *Image) map[string]any {
	body := map[string]any{
		"additionalOsCapabilities": nil,
		"baseImageArn":             img.BaseImageArn,
		"baseImageVersion":         "1.0",
		"buildPhaseOverrides":      nil,
		"buildRoleArn":             img.BuildRoleArn,
		"codeArtifact":             map[string]any{"uri": img.CodeURI},
		"cpuConfigurations":        nil,
		"createdAt":                epoch(img.CreatedAt),
		"description":              strOrNil(img.Description),
		"egressNetworkConnectors":  []any{managedConnectorARN(img.Region, "INTERNET_EGRESS")},
		"environmentVariables":     nil,
		"hooks":                    nil,
		"id":                       img.ID,
		"imageArn":                 imageARN(img.Region, img.Name),
		"imageVersion":             img.LatestVersion,
		"latestActiveImageVersion": strOrNil(img.LatestActive),
		"latestFailedImageVersion": strOrNil(img.LatestFailed),
		"logging":                  nil,
		"name":                     img.Name,
		"resources":                []any{map[string]any{"minimumMemoryInMiB": img.MemoryMiB}},
		"roleConfiguration":        nil,
		"state":                    img.State,
		"updatedAt":                epoch(img.UpdatedAt),
	}
	return body
}

// createDetail is detail plus a null tags member. Update omits tags entirely
// while Create sends it as null — a difference with no reason behind it that
// anyone can see, and exactly the kind of thing only a recording catches.
func createDetail(img *Image) map[string]any {
	body := detail(img)
	body["tags"] = nil
	return body
}

// summary is what Get returns: far smaller than Create's response, and with
// tags as an empty object rather than the null Create sends.
func summary(img *Image) map[string]any {
	return map[string]any{
		"createdAt":                epoch(img.CreatedAt),
		"id":                       img.ID,
		"imageArn":                 imageARN(img.Region, img.Name),
		"latestActiveImageVersion": strOrNil(img.LatestActive),
		"latestFailedImageVersion": strOrNil(img.LatestFailed),
		"name":                     img.Name,
		"state":                    img.State,
		"tags":                     tagsOrEmpty(img.Tags),
		"updatedAt":                epoch(img.UpdatedAt),
	}
}

// listItem is summary minus tags and updatedAt.
func listItem(img *Image) map[string]any {
	return map[string]any{
		"createdAt":                epoch(img.CreatedAt),
		"id":                       img.ID,
		"imageArn":                 imageARN(img.Region, img.Name),
		"latestActiveImageVersion": strOrNil(img.LatestActive),
		"latestFailedImageVersion": strOrNil(img.LatestFailed),
		"name":                     img.Name,
		"state":                    img.State,
	}
}

// versionDetail is returned by version get, list, and the status PATCH alike.
// It carries versionStateTimeBucket, which no model mentions.
func versionDetail(img *Image, v *Version) map[string]any {
	return map[string]any{
		"additionalOsCapabilities": nil,
		"baseImageArn":             v.BaseImageArn,
		"baseImageVersion":         v.BaseVersion,
		"buildRoleArn":             v.BuildRoleArn,
		"codeArtifact":             map[string]any{"uri": v.CodeURI},
		"cpuConfigurations":        nil,
		"createdAt":                epoch(v.CreatedAt),
		"description":              strOrNil(v.Description),
		"egressNetworkConnectors":  []any{managedConnectorARN(img.Region, "INTERNET_EGRESS")},
		"environmentVariables":     nil,
		"hooks":                    nil,
		"imageArn":                 imageARN(img.Region, img.Name),
		"imageVersion":             v.Version,
		"logging":                  nil,
		"resources":                []any{map[string]any{"minimumMemoryInMiB": v.MemoryMiB}},
		"state":                    v.State,
		"stateReason":              nil,
		"status":                   v.Status,
		"tags":                     nil,
		"updatedAt":                epoch(v.UpdatedAt),
		"versionStateTimeBucket":   stateTimeBucket(v.State, v.UpdatedAt),
	}
}

// buildListItem omits snapshotBuild; only the single-build Get carries it.
func buildListItem(img *Image, b *Build) map[string]any {
	return map[string]any{
		"architecture":          b.Architecture,
		"buildId":               b.BuildID,
		"buildState":            b.State,
		"chipset":               b.Chipset,
		"chipsetGeneration":     b.ChipsetGeneration,
		"createdAt":             epoch(b.CreatedAt),
		"imageArn":              imageARN(img.Region, img.Name),
		"imageVersion":          b.ImageVersion,
		"stateReason":           strOrNil(b.StateReason),
		"terminationReasonCode": nil,
	}
}

func buildDetail(img *Image, b *Build) map[string]any {
	body := buildListItem(img, b)
	// Sizes are the recorded ones. They are observable, so a client charting
	// image size gets a plausible shape rather than zeroes.
	body["snapshotBuild"] = map[string]any{
		"codeInstallSizeInBytes":    186400768,
		"diskSnapshotSizeInBytes":   25600000,
		"memorySnapshotSizeInBytes": 609087488,
	}
	return body
}

func strOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// tagsOrEmpty keeps an empty tag set as {} rather than null, which is what
// Get returns even on an image that has never been tagged.
func tagsOrEmpty(tags map[string]string) map[string]string {
	if tags == nil {
		return map[string]string{}
	}
	return tags
}
