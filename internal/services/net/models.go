package net

import "encoding/json"

type CreateVPCRequest struct {
	Name         string           `json:"name"`
	ProjectID    string           `json:"project_id"`
	Namespaces   []string         `json:"namespaces,omitempty"`
	StaticRoutes []StaticRouteDTO `json:"static_routes,omitempty"`
}

type StaticRouteDTO struct {
	CIDR      string `json:"cidr"`
	NextHopIP string `json:"next_hop_ip"`
	Policy    string `json:"policy,omitempty"`
}

// VPC is the h3ovn response (camelCase). GET does not return staticRoutes;
// the create response does.
type VPC struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	ProjectID    string           `json:"projectId"`
	Namespaces   []string         `json:"namespaces"`
	StaticRoutes []StaticRouteAPI `json:"staticRoutes"`
	Status       string           `json:"status"`
}

// StaticRouteAPI is a static route in h3ovn responses.
type StaticRouteAPI struct {
	CIDR      string `json:"cidr"`
	NextHopIP string `json:"nextHopIp"`
	Policy    string `json:"policy"`
}

type VPCListResponse struct {
	VPCs []VPC `json:"vpcs"`
}

type CreateNetworkRequest struct {
	Name            string   `json:"name"`
	ProjectID       string   `json:"project_id"`
	VPCID           string   `json:"vpc_id,omitempty"`
	CIDRBlock       string   `json:"cidr_block"`
	Protocol        string   `json:"protocol,omitempty"`
	ExternalSubnets []string `json:"external_subnets,omitempty"`
}

// Network is the h3ovn response: camelCase, the subnet id is "id"; it does
// not return externalSubnets. networkWire also accepts the snake_case DTO.
type Network struct {
	SubnetID    string
	Name        string
	ProjectID   string
	SubnetName  string
	GatewayID   string
	GatewayName string
	GatewayIP   string
	VPCID       string
	VPCName     string
	CIDRBlock   string
	Protocol    string
	Status      string
}

type networkWire struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ProjectID   string `json:"projectId"`
	SubnetName  string `json:"subnetName"`
	GatewayID   string `json:"gatewayId"`
	GatewayName string `json:"gatewayName"`
	GatewayIP   string `json:"gatewayIp"`
	VPCID       string `json:"vpcId"`
	VPCName     string `json:"vpcName"`
	CIDRBlock   string `json:"cidrBlock"`
	Protocol    string `json:"protocol"`
	Status      string `json:"status"`

	SubnetIDSnake    string `json:"subnet_id"`
	SubnetNameSnake  string `json:"subnet_name"`
	GatewayIDSnake   string `json:"gateway_id"`
	GatewayNameSnake string `json:"gateway_name"`
	VPCIDSnake       string `json:"vpc_id"`
	VPCNameSnake     string `json:"vpc_name"`
	CIDRBlockSnake   string `json:"cidr_block"`
	ProjectIDSnake   string `json:"project_id"`
}

func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// UnmarshalJSON accepts both the camelCase response and the snake_case DTO.
func (n *Network) UnmarshalJSON(b []byte) error {
	var w networkWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*n = Network{
		SubnetID:    first(w.ID, w.SubnetIDSnake),
		Name:        w.Name,
		ProjectID:   first(w.ProjectID, w.ProjectIDSnake),
		SubnetName:  first(w.SubnetName, w.SubnetNameSnake),
		GatewayID:   first(w.GatewayID, w.GatewayIDSnake),
		GatewayName: first(w.GatewayName, w.GatewayNameSnake),
		GatewayIP:   w.GatewayIP,
		VPCID:       first(w.VPCID, w.VPCIDSnake),
		VPCName:     first(w.VPCName, w.VPCNameSnake),
		CIDRBlock:   first(w.CIDRBlock, w.CIDRBlockSnake),
		Protocol:    w.Protocol,
		Status:      w.Status,
	}
	return nil
}

type NetworkListResponse struct {
	Networks []Network `json:"networks"`
}

type CreateEIPRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ProjectID   string `json:"project_id"`
	NetworkID   string `json:"network_id,omitempty"`
}

type AttachEIPRequest struct {
	EIPID        string `json:"eip_id"`
	ResourceID   string `json:"resource_id"`
	ResourceType string `json:"resource_type"`
}

type DetachEIPRequest struct {
	EIPID string `json:"eip_id"`
}

// UpdateEIPRequest is PATCH /api/ovn/v1/eips/{id}: "" removes the description.
type UpdateEIPRequest struct {
	Description *string `json:"description,omitempty"`
}

type EIP struct {
	ID          string `json:"id"`
	K8sName     string `json:"k8sName"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ProjectID   string `json:"projectId"`
	GatewayName string `json:"gatewayName"`
	IPAddress   string `json:"ipAddress"`
	VMID        string `json:"vmId"`
	Status      string `json:"status"`
	// ResourceType/ResourceID: current h3ovn binding fields (vm|db).
	ResourceType string `json:"resourceType,omitempty"`
	ResourceID   string `json:"resourceId,omitempty"`
	// Bound: attached (FIP exists) or reserved by a create in progress.
	// nil when the backend predates the field. h3ovn encodes camelCase
	// (boundTo); bound_to is accepted as well in case the public DTO is
	// converted to snake_case later.
	Bound        *bool       `json:"bound,omitempty"`
	BoundTo      *EIPBinding `json:"boundTo,omitempty"`
	BoundToSnake *EIPBinding `json:"bound_to,omitempty"`
}

// EIPBinding describes what an EIP is bound to.
type EIPBinding struct {
	ResourceType      string `json:"resourceType,omitempty"`
	ResourceID        string `json:"resourceId,omitempty"`
	ResourceTypeSnake string `json:"resource_type,omitempty"`
	ResourceIDSnake   string `json:"resource_id,omitempty"`
}

// binding returns (type, id) of the bound resource, empty when not bound.
func (e EIP) binding() (string, string) {
	b := e.BoundTo
	if b == nil {
		b = e.BoundToSnake
	}
	if b == nil {
		if e.ResourceID != "" {
			return e.ResourceType, e.ResourceID
		}
		return "", ""
	}
	t, id := b.ResourceType, b.ResourceID
	if t == "" {
		t = b.ResourceTypeSnake
	}
	if id == "" {
		id = b.ResourceIDSnake
	}
	return t, id
}

type EIPListResponse struct {
	EIPs []EIP `json:"eips"`
}
