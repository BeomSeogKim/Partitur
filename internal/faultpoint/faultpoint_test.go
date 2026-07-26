package faultpoint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

func TestAppendixEEdgeIDsAreCompleteAndUnique(t *testing.T) {
	edges := []EdgeID{
		EdgePrepareSnapshotToPlan,
		EdgePreparePlanToPrepared,
		EdgePreparePreparedToObserved,
		EdgeQuiesceSweptToLeaseMoved,
		EdgeQuiesceLeaseMovedToCommitLock,
		EdgePrepareQuarantinedToAbandoned,
		EdgeCancelSweptToTerminal,
		EdgeCancelSweptToQuarantined,
		EdgeCancelIntervalStoppedToTerminal,
		EdgeCancelFenceDecidedToTerminal,
		EdgeCancelTerminalToLeaseRemoved,
		EdgeSupersedeSweptToApproved,
		EdgeSupersedeIntervalStoppedToApproved,
		EdgeSupersedeFenceDecidedToApproved,
		EdgeSupersedeApprovedToLeaseRemoved,
		EdgeAuthorityGrantedToLeaseCreated,
		EdgeLaunchAdapterMarkerHeldToIdentity,
		EdgeLaunchAdapterIdentityPublishedToRecorded,
		EdgeLaunchAdapterRecordedToGate,
		EdgeLaunchCriterionMarkerHeldToIdentity,
		EdgeLaunchCriterionIdentityToRecorded,
		EdgeLaunchCriterionRecordedToGate,
	}
	if len(edges) != 22 {
		t.Fatalf("edge count = %d, want 22", len(edges))
	}
	seen := map[EdgeID]bool{}
	for _, edge := range edges {
		if edge == "" || seen[edge] {
			t.Fatalf("empty or duplicate edge id %q", edge)
		}
		seen[edge] = true
	}
}

func TestBoundaryPointIDsAreSemanticAndUnique(t *testing.T) {
	points := []PointID{
		PointPrepareObserved,
		PointQuiesceSessionsSwept,
		PointQuiesceCommitLockHeld,
		PointCancelSessionsSwept,
		PointCancelFenceDecided,
		PointSupersedeSessionsSwept,
		PointSupersedeFenceDecided,
		PointLaunchAdapterMarkerHeld,
		PointLaunchAdapterGateReleased,
		PointLaunchCriterionMarkerHeld,
		PointLaunchCriterionGateReleased,
	}
	seen := map[PointID]bool{}
	for _, point := range points {
		if point == "" || seen[point] {
			t.Fatalf("empty or duplicate point id %q", point)
		}
		seen[point] = true
	}
}

func TestPackageHasNoGlobalProbeSetter(t *testing.T) {
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info os.FileInfo) bool {
		return info.Name() != "faultpoint_test.go"
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range packages["faultpoint"].Files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == "SetProbe" {
				t.Fatal("package-level SetProbe is forbidden")
			}
		}
	}
}
