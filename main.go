package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"io/ioutil"
	"strconv"
	"net"

	"k8s.io/client-go/dynamic"
	"github.com/vishvananda/netlink"
	//"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

const (
	SysBusPci       = "/sys/bus/pci/devices"
	NetDirectory    = "/sys/class/net"
	sriovConfigured = "/sriov_numvfs"
)

type PCIInfo struct {
	PfName string
	VfId   int
	Mac    net.HardwareAddr
}

type Resource struct {
	Name    string   `json:"name"`
	Devices []Device `json:"devices"`
}

type Device struct {
	PCI string `json:"pci"`
	MAC string `json:"mac"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("ERROR: Arguments of format <net_name>,<resource_name>,<vf_pci1>,<vf_pci2>,...,<vf_pciN> is required")
		os.Exit(22)
	}
	name := os.Args[1]
	args := os.Args[2:]

	var resources []Resource
	for _, arg := range args {
		argSplit := strings.Split(arg, ",")
		if len(argSplit) < 3 {
			fmt.Println("ERROR: Invaid arg format ", args)
			os.Exit(22)
		}

		var devices []Device
		//netName := argSplit[0]
		resName := argSplit[1]
		pciList := argSplit[2:]
		for _, pci := range pciList {
			pciInfo, err := getVfMac(pci)
			if err != nil {
				fmt.Println("ERROR: Failed to get mac address of VF ", pci)
				os.Exit(1)
			}
			devices = append(devices, Device{PCI: pci, MAC: pciInfo.Mac.String()})
		}
		resources = append(resources, Resource{Name: resName, Devices: devices})
	}

	fmt.Println(resources)
	err := createCR(name, resources)
	if err != nil {
		fmt.Println("ERROR: Failed to create CR - ", err)
		os.Exit(1)
	}
}

func createCR(name string, resources []Resource) error {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}

	dyClient, err := dynamic.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	coClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	namespace := os.Getenv("NAMESPACE")
	nodename := os.Getenv("NODENAME")

	resInterface := []interface{}{}
	for _, item := range resources {
		devInterface := []interface{}{}
		for _, devItem := range item.Devices {
			dev := map[string]interface{}{
				"pci": devItem.PCI,
				"mac": devItem.MAC,
			}
			devInterface = append(devInterface, dev)
		}
		res := map[string]interface{}{
			"name": item.Name,
			"devices": devInterface,
		}
		resInterface = append(resInterface, res)
	}

	pod, err := coClient.CoreV1().Pods(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		panic(err)
	}

	uid := pod.UID
	fmt.Println(pod.UID)
	fmt.Println(pod.Name)
	fmt.Println(pod.Namespace)
	owners := []interface{}{}
	owner := map[string]interface{}{
		"apiVersion":         "v1",
		"controller":         true,
		"blockOwnerDeletion": false,
		"kind":               "Pod",
		"name":               name,
		"uid":               uid,
	}
	owners = append(owners, owner)

	macRes := schema.GroupVersionResource{Group: "examplecnf.openshift.io", Version: "v1", Resource: "cnfappmacs"}
	mac := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "examplecnf.openshift.io/v1",
			"kind":       "CNFAppMac",
			"metadata":   map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"ownerReferences": owners,
			},
			"spec": map[string]interface{}{
				"resources": resInterface,
				"node" :     nodename,
				"hostname":  name,
			},
		},
	}
	_, err = dyClient.Resource(macRes).Namespace(namespace).Create(context.TODO(), mac, metav1.CreateOptions{})
	if err != nil {
		panic(err.Error())
	}

	return err
}

func getVfMac(pci string) (*PCIInfo, error) {
	pfName, err := GetPfName(pci)
	vfId, err := GetVfid(pci, pfName)
	if err != nil {
		return nil, err
	}

	pfLink, err := netlink.LinkByName(pfName)
	if err != nil {
		return nil, err
	}

	vfInfo := getVfInfo(pfLink, vfId)
	pciInfo := &PCIInfo {
		PfName: pfName,
		VfId: vfId,
		Mac: vfInfo.Mac,
	}
	return pciInfo, nil
}

func getVfInfo(link netlink.Link, id int) *netlink.VfInfo {
	attrs := link.Attrs()
	for _, vf := range attrs.Vfs {
		if vf.ID == id {
			return &vf
		}
	}
	return nil
}

// GetSriovNumVfs takes in a PF name(ifName) as string and returns number of VF configured as int
func GetSriovNumVfs(ifName string) (int, error) {
	var vfTotal int

	sriovFile := filepath.Join(NetDirectory, ifName, "device", sriovConfigured)
	if _, err := os.Lstat(sriovFile); err != nil {
		return vfTotal, fmt.Errorf("failed to open the sriov_numfs of device %q: %v", ifName, err)
	}

	data, err := ioutil.ReadFile(sriovFile)
	if err != nil {
		return vfTotal, fmt.Errorf("failed to read the sriov_numfs of device %q: %v", ifName, err)
	}

	if len(data) == 0 {
		return vfTotal, fmt.Errorf("no data in the file %q", sriovFile)
	}

	sriovNumfs := strings.TrimSpace(string(data))
	vfTotal, err = strconv.Atoi(sriovNumfs)
	if err != nil {
		return vfTotal, fmt.Errorf("failed to convert sriov_numfs(byte value) to int of device %q: %v", ifName, err)
	}

	return vfTotal, nil
}

// GetVfid takes in VF's PCI address(addr) and pfName as string and returns VF's ID as int
func GetVfid(addr string, pfName string) (int, error) {
	var id int
	vfTotal, err := GetSriovNumVfs(pfName)
	if err != nil {
		return id, err
	}
	for vf := 0; vf <= vfTotal; vf++ {
		vfDir := filepath.Join(NetDirectory, pfName, "device", fmt.Sprintf("virtfn%d", vf))
		_, err := os.Lstat(vfDir)
		if err != nil {
			continue
		}
		pciinfo, err := os.Readlink(vfDir)
		if err != nil {
			continue
		}
		pciaddr := filepath.Base(pciinfo)
		if pciaddr == addr {
			return vf, nil
		}
	}
	return id, fmt.Errorf("unable to get VF ID with PF: %s and VF pci address %v", pfName, addr)
}

// GetPfName returns PF net device name of a given VF pci address
func GetPfName(vf string) (string, error) {
	pfSymLink := filepath.Join(SysBusPci, vf, "physfn", "net")
	_, err := os.Lstat(pfSymLink)
	if err != nil {
		return "", err
	}

	files, err := ioutil.ReadDir(pfSymLink)
	if err != nil {
		return "", err
	}

	if len(files) < 1 {
		return "", fmt.Errorf("PF network device not found")
	}

	return strings.TrimSpace(files[0].Name()), nil
}
