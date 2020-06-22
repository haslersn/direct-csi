// This file is part of MinIO Kubernetes Cloud
// Copyright (c) 2020 MinIO, Inc.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package node

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"github.com/minio/jbod-csi-driver/pkg/topology"
	"github.com/minio/jbod-csi-driver/pkg/volume"

	"google.golang.org/grpc/status"
	"google.golang.org/grpc/codes"
)

const MaxVolumes = 10000

func NewNodeServer(identity, nodeID, rack, zone, region string) (*NodeServer, error) {
	return &NodeServer{
		NodeID:   nodeID,
		Identity: identity,
		Rack:     rack,
		Zone:     zone,
		Region:   region,
	}, nil
}

type NodeServer struct {
	NodeID   string
	Identity string
	Rack     string
	Zone     string
	Region   string
}

func (n *NodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	topology := &csi.Topology{
		Segments: map[string]string{
			topology.TopologyDriverIdentity: n.Identity,
			topology.TopologyDriverNode:     n.NodeID,
			topology.TopologyDriverRack:     n.Rack,
			topology.TopologyDriverZone:     n.Zone,
			topology.TopologyDriverRegion:   n.Region,
		},
	}

	return &csi.NodeGetInfoResponse{
		NodeId:             n.NodeID,
		MaxVolumesPerNode:  MaxVolumes,
		AccessibleTopology: topology,
	}, nil
}

func (n *NodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	nodeCap := func(cap csi.NodeServiceCapability_RPC_Type) *csi.NodeServiceCapability {
		glog.Infof("Using node capability %v", cap)

		return &csi.NodeServiceCapability{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: cap,
				},
			},
		}
	}

	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			nodeCap(csi.NodeServiceCapability_RPC_VOLUME_CONDITION),
			nodeCap(csi.NodeServiceCapability_RPC_GET_VOLUME_STATS),
			nodeCap(csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME),
		},
	}, nil
}

func (n *NodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	vID := req.GetVolumeId()
	ro := req.GetReadonly()
	targetPath := req.GetTargetPath()
	stagingPath := req.GetStagingTargetPath()
	vCtx := req.GetVolumeContext()
	vCap := req.GetVolumeCapability()

	if vID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID missing in request")
	}

	vol, err := volume.GetVolume(ctx, vID)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}

	if vol.StagingPath != stagingPath {
		return nil, status.Error(codes.FailedPrecondition, "volume staging target path is empty or incorrect")
	}

	if access, ok := vol.ContainsTargetPaths(targetPath); ok {
		if access.Matches(req) {
			return &csi.NodePublishVolumeResponse{}, nil
		}
		return nil, status.Error(codes.AlreadyExists, "cannot reprovision volume at same path but different parameters")
	}

	if vCap == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability missing in request")
	}

	if vCap.GetBlock() != nil && vCap.GetMount() != nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability request contains both mount and block access")
	}

	if vCap.GetBlock() == nil && vCap.GetMount() == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability request contains neither mount and block access")
	}

	if vCap.GetBlock() != nil {
		if !vol.IsBlockAccessible() {
			return nil, status.Error(codes.InvalidArgument, "volume does not support block access")
		}

		if err := vol.Bind(targetPath, ro, vCtx); err != nil {
			if _, ok := status.FromError(err); ok {
				return nil, err
			}
			return nil, status.Error(codes.Internal, err.Error())
		}
		glog.V(5).Infof("published block access request for volume %s successfully", vID)
	}

	if vMount := vCap.GetMount(); vMount != nil {
		if !vol.IsMountAccessible() {
			return nil, status.Error(codes.InvalidArgument, "volume does not support mount access")
		}

		fs := vMount.GetFsType()
		flags := vMount.GetMountFlags()

		if err := vol.Mount(targetPath, fs, flags, ro, vCtx); err != nil {
			if _, ok := status.FromError(err); ok {
				return nil, err
			}
			return nil, status.Error(codes.Internal, err.Error())
		}
		glog.V(5).Infof("published mount access request for volume %s successfully", vID)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}
