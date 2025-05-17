package main

import (
	"fmt"
	"os"

	"github.com/pulumi/pulumi-azure-native-sdk/compute/v2"
	"github.com/pulumi/pulumi-azure-native-sdk/network/v2"
	"github.com/pulumi/pulumi-azure-native-sdk/resources/v2"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

type Image struct {
	Offer     string
	Publisher string
	Sku       string
	Version   string
}

type NIC struct {
	EnableAcceleratedNetworking bool
	EnableIPForwarding          bool
	Name                        string
	PipName                     string
	SnetName                    string
}

type NICMAP struct {
	Nic0 string
	Nic1 string
	Nic2 string
}

type NSG struct {
	Name  string
	Rules []Rule
}

type PIP struct {
	Name string
}

type Route struct {
	AddressPrefix    string
	Name             string
	NextHopIpAddress string
	NextHopType      string
}

type RT struct {
	Name                       string
	DisableBgpRoutePropagation bool
	Routes                     []Route
}

type Rule struct {
	Access                   string
	DestinationAddressPrefix string
	DestinationPortRange     string
	Direction                string
	Name                     string
	Priority                 int
	Protocol                 string
	SourceAddressPrefix      string
	SourcePortRange          string
}

type SNET struct {
	AddressPrefix string
	Name          string
	NSGName       string
	RTName        string
}

type Tags struct {
	Automation string
	Solution   string
}

type VM struct {
	AdminPassword      string
	AdminUsername      string
	ComputerName       string
	Image              Image
	NicMap             NICMAP
	StorageAccountType string
	VmSize             string
}

type VNET struct {
	AddressSpace string
	NIC          []NIC
	NSG          []NSG
	PIP          []PIP
	RT           []RT
	SNET         []SNET
}

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {

		// Export the project's readme.
		readmeBytes, err := os.ReadFile("./README.md")
		if err != nil {
			return fmt.Errorf("failed to read readme: %w", err)
		}
		ctx.Export("readme", pulumi.String(string(readmeBytes)))

		// Define a variable for Pulumi configuration.
		cfg := config.New(ctx, "")

		// Define a variable for resource tagging. This sources from Pulumi configuration via the Tags type struct declaration.
		var tags Tags
		cfg.RequireObject("tags", &tags)

		// Define a variable for network properties. This sources from Pulumi configuration via the VNET type struct declaration.
		var vnet VNET
		cfg.RequireObject("vnet", &vnet)

		// Define a variable for VM properties. This sources from Pulumi configuration via the VM type struct declaration.
		var vm VM
		cfg.RequireObject("vm", &vm)

		// Define required tags for the project.
		requiredTags := pulumi.StringMap{
			"automation": pulumi.String(tags.Automation),
			"solution":   pulumi.String(tags.Solution),
		}

		// Define the standard nameSuffix variable to use for naming Pulumi resources.
		nameSuffix := "panos-vm-" + ctx.Stack() + "-"

		// Create an Azure Resource Group
		resourceGroup, err := resources.NewResourceGroup(ctx, "rg-"+nameSuffix, &resources.ResourceGroupArgs{
			Tags: requiredTags,
		})
		if err != nil {
			return err
		}

		// Create Network Security Groups and Security Rules.
		nsgMap := make(map[string]*network.NetworkSecurityGroup)
		for _, nsg := range vnet.NSG {
			var securityRules network.SecurityRuleTypeArray
			for _, rule := range nsg.Rules {
				securityRules = append(securityRules, network.SecurityRuleTypeArgs{
					Access:                   pulumi.String(rule.Access),
					DestinationAddressPrefix: pulumi.String(rule.DestinationAddressPrefix),
					DestinationPortRange:     pulumi.String(rule.DestinationPortRange),
					Direction:                pulumi.String(rule.Direction),
					Name:                     pulumi.String(rule.Name),
					Priority:                 pulumi.Int(rule.Priority),
					Protocol:                 pulumi.String(rule.Protocol),
					SourceAddressPrefix:      pulumi.String(rule.SourceAddressPrefix),
					SourcePortRange:          pulumi.String(rule.SourcePortRange),
				})
			}

			nsgResource, err := network.NewNetworkSecurityGroup(ctx, "nsg-"+nsg.Name+"-"+nameSuffix, &network.NetworkSecurityGroupArgs{
				ResourceGroupName: resourceGroup.Name,
				SecurityRules:     securityRules,
				Tags:              requiredTags,
			},
				pulumi.DependsOn([]pulumi.Resource{resourceGroup}),
				pulumi.Parent(resourceGroup),
			)
			if err != nil {
				return err
			}
			nsgMap[nsg.Name] = nsgResource
		}

		/*
			// Export the nsgMap to a stack output. For debugging.
			nsgMapOutput := pulumi.StringMap{}
			for key, nsg := range nsgMap {
				nsgMapOutput[key] = nsg.ID().ToStringOutput()
			}
			ctx.Export("nsgMap", nsgMapOutput)
		*/

		// Create Route Tables and Routes.
		rtMap := make(map[string]*network.RouteTable)
		for _, rt := range vnet.RT {
			var routes network.RouteTypeArray
			for _, route := range rt.Routes {
				routes = append(routes, network.RouteTypeArgs{
					AddressPrefix:    pulumi.String(route.AddressPrefix),
					Name:             pulumi.String(route.Name),
					NextHopType:      pulumi.String(route.NextHopType),
					NextHopIpAddress: pulumi.String(route.NextHopIpAddress),
				})
			}

			// Ensure route tables depend on the network security groups.
			routeTableDependencies := []pulumi.Resource{resourceGroup}
			for _, nsgResource := range nsgMap {
				routeTableDependencies = append(routeTableDependencies, nsgResource)
			}

			rtResource, err := network.NewRouteTable(ctx, "rt-"+rt.Name+"-"+nameSuffix, &network.RouteTableArgs{
				DisableBgpRoutePropagation: pulumi.Bool(rt.DisableBgpRoutePropagation),
				ResourceGroupName:          resourceGroup.Name,
				Routes:                     routes,
				Tags:                       requiredTags,
			},
				pulumi.DependsOn(routeTableDependencies),
				pulumi.Parent(resourceGroup),
			)
			if err != nil {
				return err
			}
			rtMap[rt.Name] = rtResource
		}

		/*
			// Export the rtMap to a stack output.  For debugging.
			rtMapOutput := pulumi.StringMap{}
			for key, rt := range rtMap {
				rtMapOutput[key] = rt.ID().ToStringOutput()
			}
			ctx.Export("rtMap", rtMapOutput)
		*/

		// Define the nsgMap and rtMap as pulumi resources, so they can be used as dependencies for the virtual network resource.
		virtualNetworkDependencies := []pulumi.Resource{}
		for _, nsg := range nsgMap {
			virtualNetworkDependencies = append(virtualNetworkDependencies, nsg)
		}
		for _, rt := range rtMap {
			virtualNetworkDependencies = append(virtualNetworkDependencies, rt)
		}

		// Create a virtual network.
		virtualNetwork, err := network.NewVirtualNetwork(ctx, "vnet-"+nameSuffix, &network.VirtualNetworkArgs{
			AddressSpace: &network.AddressSpaceArgs{
				AddressPrefixes: pulumi.StringArray{
					pulumi.String(vnet.AddressSpace),
				},
			},
			ResourceGroupName: resourceGroup.Name,
			Tags:              requiredTags,
		},
			pulumi.DependsOn(virtualNetworkDependencies),
			pulumi.Parent(resourceGroup),
		)
		if err != nil {
			return err
		}

		// Create Subnets and associate with Network Security Groups and Route Tables.
		snetMap := make(map[string]*network.Subnet)
		snetResources := []pulumi.Resource{}
		for _, snet := range vnet.SNET {
			snetResource, err := network.NewSubnet(ctx, "snet-"+snet.Name, &network.SubnetArgs{
				AddressPrefix: pulumi.String(snet.AddressPrefix),
				NetworkSecurityGroup: &network.NetworkSecurityGroupTypeArgs{
					Id: nsgMap[snet.NSGName].ID(),
				},
				ResourceGroupName: resourceGroup.Name,
				RouteTable: &network.RouteTableTypeArgs{
					Id: rtMap[snet.RTName].ID(),
				},
				VirtualNetworkName: virtualNetwork.Name,
			},
				pulumi.DependsOn(virtualNetworkDependencies),
				pulumi.Parent(virtualNetwork),
			)
			if err != nil {
				return err
			}
			snetMap[snet.Name] = snetResource
			snetResources = append(snetResources, snetResource)
		}

		/*
			// Export the subnetMap to a stack output. For debugging.
			snetMapOutput := pulumi.StringMap{}
			for key, snet := range snetMap {
				snetMapOutput[key] = snet.ID().ToStringOutput()
			}
			ctx.Export("snetMap", snetMapOutput)
		*/

		// Create Public IP Addesses.
		pipMap := make(map[string]*network.PublicIPAddress)
		pipResources := []pulumi.Resource{}
		for _, pip := range vnet.PIP {
			pipResource, err := network.NewPublicIPAddress(ctx, "pip-"+pip.Name+"-"+nameSuffix, &network.PublicIPAddressArgs{
				PublicIPAllocationMethod: pulumi.String("Static"),
				ResourceGroupName:        resourceGroup.Name,
				Sku: &network.PublicIPAddressSkuArgs{
					Name: pulumi.String("Standard"),
					Tier: pulumi.String("Regional"),
				},
				Tags: requiredTags,
			},
				pulumi.DependsOn(snetResources),
				pulumi.Parent(resourceGroup),
			)
			if err != nil {
				return err
			}
			pipMap[pip.Name] = pipResource
			pipResources = append(pipResources, pipResource)
		}

		/*
			// Export the pipMap to a stack output. For debugging.
			pipMapOutput := pulumi.StringMap{}
			for key, pip := range pipMap {
				pipMapOutput[key] = pip.ID().ToStringOutput()
			}
			ctx.Export("pipMap", pipMapOutput)
		*/

		// Create NICs.
		nicMap := make(map[string]*network.NetworkInterface)
		nicResources := []pulumi.Resource{}
		for _, nic := range vnet.NIC {
			ipConfigArgs := &network.NetworkInterfaceIPConfigurationArgs{
				Name: pulumi.String("ipconfig"),
				Subnet: &network.SubnetTypeArgs{
					Id: snetMap[nic.SnetName].ID(),
				},
			}

			// Check if pipMap contains the nic.PipName
			if pip, exists := pipMap[nic.PipName]; exists {
				ipConfigArgs.PublicIPAddress = &network.PublicIPAddressTypeArgs{
					Id: pip.ID(),
				}
			}

			nicResource, err := network.NewNetworkInterface(ctx, "nic-"+nic.Name+"-"+nameSuffix, &network.NetworkInterfaceArgs{
				EnableAcceleratedNetworking: pulumi.Bool(nic.EnableAcceleratedNetworking),
				EnableIPForwarding:          pulumi.Bool(nic.EnableIPForwarding),
				NicType:                     pulumi.String("Standard"),
				IpConfigurations: network.NetworkInterfaceIPConfigurationArray{
					*ipConfigArgs,
				},
				ResourceGroupName: resourceGroup.Name,
				Tags:              requiredTags,
			},
				pulumi.DependsOn(pipResources),
				pulumi.Parent(resourceGroup),
			)
			if err != nil {
				return err
			}
			nicMap[nic.Name] = nicResource
			nicResources = append(nicResources, nicResource)
		}

		/*
			// Export the pipMap to a stack output. For debugging.
			nicMapOutput := pulumi.StringMap{}
			for key, nic := range nicMap {
				nicMapOutput[key] = nic.ID().ToStringOutput()
			}
			ctx.Export("nicMap", nicMapOutput)
		*/

		// Create a random ID for the OS disk
		randomOsDiskId, err := random.NewRandomString(ctx, "random-os-disk-id", &random.RandomStringArgs{
			Length:     pulumi.Int(8),
			Lower:      pulumi.Bool(true),
			MinLower:   pulumi.Int(4),
			MinNumeric: pulumi.Int(4),
			Numeric:    pulumi.Bool(true),
			Special:    pulumi.Bool(false),
			Upper:      pulumi.Bool(false),
		},
			pulumi.DependsOn(nicResources),
		)
		if err != nil {
			return err
		}

		// Create a virtual machine.
		virtualMachine, err := compute.NewVirtualMachine(ctx, "vm-"+tags.Solution+"-prod-", &compute.VirtualMachineArgs{
			HardwareProfile: compute.HardwareProfileArgs{
				VmSize: pulumi.String(vm.VmSize),
			},
			NetworkProfile: compute.NetworkProfileArgs{
				NetworkInterfaces: compute.NetworkInterfaceReferenceArray{
					compute.NetworkInterfaceReferenceArgs{
						Id:      nicMap[vm.NicMap.Nic0].ID(),
						Primary: pulumi.Bool(true),
					},
					compute.NetworkInterfaceReferenceArgs{
						Id:      nicMap[vm.NicMap.Nic1].ID(),
						Primary: pulumi.Bool(false),
					},
					compute.NetworkInterfaceReferenceArgs{
						Id:      nicMap[vm.NicMap.Nic2].ID(),
						Primary: pulumi.Bool(false),
					},
				},
			},
			OsProfile: compute.OSProfileArgs{
				AdminPassword:            pulumi.String(vm.AdminPassword),
				AdminUsername:            pulumi.String(vm.AdminUsername),
				AllowExtensionOperations: pulumi.Bool(true),
				ComputerName:             pulumi.String(vm.ComputerName),
				LinuxConfiguration: compute.LinuxConfigurationArgs{
					DisablePasswordAuthentication: pulumi.Bool(false),
					EnableVMAgentPlatformUpdates:  pulumi.Bool(true),
					ProvisionVMAgent:              pulumi.Bool(true),
				},
			},
			Plan: &compute.PlanArgs{
				Name:      pulumi.String(vm.Image.Sku),
				Product:   pulumi.String(vm.Image.Offer),
				Publisher: pulumi.String(vm.Image.Publisher),
			},
			ResourceGroupName: resourceGroup.Name,
			StorageProfile: compute.StorageProfileArgs{
				ImageReference: compute.ImageReferenceArgs{
					Offer:     pulumi.String(vm.Image.Offer),
					Publisher: pulumi.String(vm.Image.Publisher),
					Sku:       pulumi.String(vm.Image.Sku),
					Version:   pulumi.String(vm.Image.Version),
				},
				OsDisk: compute.OSDiskArgs{
					Caching:      compute.CachingTypesReadWrite,
					CreateOption: pulumi.String("FromImage"),
					DeleteOption: pulumi.String("Delete"),
					DiskSizeGB:   pulumi.Int(127),
					ManagedDisk: compute.ManagedDiskParametersArgs{
						StorageAccountType: pulumi.String(vm.StorageAccountType),
					},
					Name: pulumi.Sprintf("os-%s%s", nameSuffix, randomOsDiskId.Result),
				},
			},
			Tags: requiredTags,
		},
			pulumi.DependsOn(append(nicResources, randomOsDiskId)),
			pulumi.Parent(resourceGroup),
		)
		ctx.Value(virtualMachine)
		if err != nil {
			return err
		}

		return nil
	})
}

