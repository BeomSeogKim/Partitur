package docclaim

type baseline struct {
	documentPath string
	gitBlob      string
}

type claim struct {
	documentPath    string
	markerID        string
	evidencePackage string
	evidenceTest    string
}

type manifest struct {
	baselines []baseline
	claims    []claim
}

// documentationClaimManifest is the canonical P5 registry. It remains exactly
// empty until the reviewed marking pass supplies the complete population.
var documentationClaimManifest = manifest{}
