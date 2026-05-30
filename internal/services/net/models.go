package net

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

type VPC struct {
	ID         string   `json:"id"`
	K8sUID     string   `json:"k8sUid"`
	Name       string   `json:"name"`
	ProjectID  string   `json:"projectId"`
	Namespace  string   `json:"namespace"`
	Namespaces []string `json:"namespaces"`
	Status     string   `json:"status"`
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

type Network struct {
	SubnetID    string `json:"subnet_id"`
	SubnetName  string `json:"subnet_name"`
	GatewayID   string `json:"gateway_id"`
	GatewayName string `json:"gateway_name"`
	VPCID       string `json:"vpc_id"`
	VPCName     string `json:"vpc_name"`
	CIDRBlock   string `json:"cidr_block"`
	Protocol    string `json:"protocol"`
	Status      string `json:"status"`
}

type NetworkListResponse struct {
	Networks []Network `json:"networks"`
}

type CreateEIPRequest struct {
	Name      string `json:"name"`
	ProjectID string `json:"project_id"`
	NetworkID string `json:"network_id,omitempty"`
}

type AttachEIPRequest struct {
	EIPID        string `json:"eip_id"`
	ResourceID   string `json:"resource_id"`
	ResourceType string `json:"resource_type"`
}

type DetachEIPRequest struct {
	EIPID string `json:"eip_id"`
}

type EIP struct {
	ID          string `json:"id"`
	K8sUID      string `json:"k8sUid"`
	K8sName     string `json:"k8sName"`
	Name        string `json:"name"`
	ProjectID   string `json:"projectId"`
	Namespace   string `json:"namespace"`
	GatewayName string `json:"gatewayName"`
	IPAddress   string `json:"ipAddress"`
	VMID        string `json:"vmId"`
	FIPName     string `json:"fipName"`
	Status      string `json:"status"`
}

type EIPListResponse struct {
	EIPs []EIP `json:"eips"`
}
