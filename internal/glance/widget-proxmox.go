package glance

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"
)

var proxmoxStatsWidgetTemplate = mustParseTemplate("proxmox.html", "widget-base.html")

type proxmoxWidget struct {
	widgetBase    `yaml:",inline"`
	URL           string             `yaml:"url"`
	Token         string             `yaml:"token"`
	Secret        string             `yaml:"secret"`
	HideSwap      bool               `yaml:"hide-swap"`
	HideStopped   bool               `yaml:"hide-stopped"`
	AllowInsecure bool               `yaml:"allow-insecure"`
	Nodes         []proxmoxNodeStats `yaml:"-"`
}

type proxmoxNodeStats struct {
	Name        string
	IsReachable bool
	HideSwap    bool
	HideStopped bool
	BootTime    time.Time
	Hostname    string
	Platform    string

	CPU struct {
		LoadIsAvailable bool
		Load1Percent    uint8
		Load15Percent   uint8

		TemperatureIsAvailable bool
		TemperatureC           uint8
	}

	Memory struct {
		IsAvailable bool
		TotalMB     uint64
		UsedMB      uint64
		UsedPercent uint8

		SwapIsAvailable bool
		SwapTotalMB     uint64
		SwapUsedMB      uint64
		SwapUsedPercent uint8
	}

	Disk        nodeStorageInfo
	Mountpoints []nodeStorageInfo
	VMs         []proxmoxVMStats
}

type nodeStorageInfo struct {
	Path        string
	Name        string
	TotalMB     uint64
	UsedMB      uint64
	UsedPercent uint8
}

type singleResponse[T any] struct {
	Data T `json:"data"`
}

type multipleResponse[T any] struct {
	Data []T `json:"data"`
}

type proxmoxClusterResource struct {
	CPU        float64 `json:"cpu"`
	CgroupMode int     `json:"cgroup-mode"`
	Content    string  `json:"content"`
	Disk       uint64  `json:"disk"`
	DiskRead   int64   `json:"diskread"`
	DiskWrite  int64   `json:"diskwrite"`
	ID         string  `json:"id"`
	Level      string  `json:"level"`
	MaxCPU     int     `json:"maxcpu"`
	MaxDisk    uint64  `json:"maxdisk"`
	MaxMem     int64   `json:"maxmem"`
	Mem        int64   `json:"mem"`
	Name       string  `json:"name"`
	NetIn      int64   `json:"netin"`
	NetOut     int64   `json:"netout"`
	Node       string  `json:"node"`
	PluginType string  `json:"plugintype"`
	SDN        string  `json:"sdn"`
	Shared     int     `json:"shared"`
	Status     string  `json:"status"`
	Storage    string  `json:"storage"`
	Template   int     `json:"template"`
	Type       string  `json:"type"`
	Uptime     int64   `json:"uptime"`
	VMID       int     `json:"vmid"`
}

type proxmoxNodeStatus struct {
	PveVersion string   `json:"pveversion"`
	Kversion   string   `json:"kversion"`
	Wait       float64  `json:"wait"`
	Uptime     int64    `json:"uptime"`
	LoadAvg    []string `json:"loadavg"`
	Cpu        float64  `json:"cpu"`
	Idle       int      `json:"idle"`

	Swap struct {
		Free  uint64 `json:"free"`
		Used  uint64 `json:"used"`
		Total uint64 `json:"total"`
	} `json:"swap"`

	CpuInfo struct {
		Model   string `json:"model"`
		Cpus    int    `json:"cpus"`
		Hvm     string `json:"hvm"`
		UserHz  int    `json:"user_hz"`
		Flags   string `json:"flags"`
		Cores   int    `json:"cores"`
		Sockets int    `json:"sockets"`
		Mhz     string `json:"mhz"`
	} `json:"cpuinfo"`

	Memory struct {
		Free  uint64 `json:"free"`
		Used  uint64 `json:"used"`
		Total uint64 `json:"total"`
	} `json:"memory"`

	RootFs struct {
		Available int64 `json:"avail"`
		Total     int64 `json:"total"`
		Used      int64 `json:"used"`
		Free      int64 `json:"free"`
	} `json:"rootfs"`
}

func (widget *proxmoxWidget) initialize() error {
	widget.withTitle("PVE监控").withCacheDuration(15 * time.Second)

	if widget.URL == "" {
		return errors.New("URL is required")
	}

	return nil
}

type proxmoxVMStats struct {
	VMID          int
	Name          string
	Status        string
	CPUPercent    uint8
	MemoryTotalMB uint64
	MemoryUsedMB  uint64
	MemoryPercent uint8
	DiskTotalMB   uint64
	DiskUsedMB    uint64
	DiskPercent   uint8
	Uptime        int64
	UptimeStr     string
}

func (widget *proxmoxWidget) update(context.Context) {
	resources, err := fetchProxmoxClusterResources(widget)
	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	nodes := make([]proxmoxNodeStats, 0)
	for _, resource := range resources {
		if resource.Type != "node" {
			continue
		}

		var node proxmoxNodeStats
		node.Name = resource.Node
		node.BootTime = time.Unix(time.Now().Unix()-resource.Uptime, 0)
		node.HideSwap = widget.HideSwap
		node.HideStopped = widget.HideStopped

		status, err := fetchProxmoxNodeStatus(widget, resource.Node)
		if err != nil {
			continue
		}

		node.IsReachable = true
		node.Platform = status.PveVersion

		if len(status.LoadAvg) == 3 {
			node.CPU.LoadIsAvailable = true

			load1, _ := strconv.ParseFloat(status.LoadAvg[0], 64)
			node.CPU.Load1Percent = uint8(math.Min(load1*100/float64(status.CpuInfo.Cores), 100))

			load15, _ := strconv.ParseFloat(status.LoadAvg[2], 64)
			node.CPU.Load15Percent = uint8(math.Min(load15*100/float64(status.CpuInfo.Cores), 100))
		}

		node.Memory.IsAvailable = true
		node.Memory.TotalMB = status.Memory.Total / 1024 / 1024
		node.Memory.UsedMB = status.Memory.Used / 1024 / 1024

		if node.Memory.TotalMB > 0 {
			node.Memory.UsedPercent = uint8(math.Min(float64(node.Memory.UsedMB)/float64(node.Memory.TotalMB)*100, 100))
		}

		node.Memory.SwapIsAvailable = true
		node.Memory.SwapTotalMB = status.Swap.Total / 1024 / 1024
		node.Memory.SwapUsedMB = status.Swap.Used / 1024 / 1024

		if node.Memory.SwapTotalMB > 0 {
			node.Memory.SwapUsedPercent = uint8(math.Min(float64(node.Memory.SwapUsedMB)/float64(node.Memory.SwapTotalMB)*100, 100))
		}

		node.Disk = nodeStorageInfo{
			TotalMB: resource.MaxDisk / 1024 / 1024,
			UsedMB:  resource.Disk / 1024 / 1024,
		}
		node.Disk.UsedPercent = uint8(math.Min(float64(node.Disk.UsedMB)/float64(node.Disk.TotalMB)*100, 100))

		for _, storage := range resources {
			if storage.Type != "storage" || storage.Node != node.Name {
				continue
			}

			storageInfo := nodeStorageInfo{
				Path:    storage.ID,
				Name:    storage.Storage,
				TotalMB: storage.MaxDisk / 1024 / 1024,
				UsedMB:  storage.Disk / 1024 / 1024,
			}

			if storageInfo.TotalMB > 0 {
				storageInfo.UsedPercent = uint8(math.Min(float64(storageInfo.UsedMB)/float64(storageInfo.TotalMB)*100, 100))
			}

			node.Mountpoints = append(node.Mountpoints, storageInfo)
		}

		// 获取虚拟机信息
		node.VMs = make([]proxmoxVMStats, 0)
		for _, vm := range resources {
			if vm.Type != "qemu" || vm.Node != node.Name {
				continue
			}

			// 如果配置了隐藏已停止的虚拟机，且当前虚拟机状态为stopped或unknown，则跳过
			if node.HideStopped && (vm.Status == "stopped" || vm.Status == "unknown") {
				continue
			}

			vmStats := proxmoxVMStats{
				VMID:          vm.VMID,
				Name:          vm.Name,
				Status:        vm.Status,
				CPUPercent:    uint8(math.Min(vm.CPU*100, 100)),
				MemoryTotalMB: uint64(vm.MaxMem) / 1024 / 1024,
				MemoryUsedMB:  uint64(vm.Mem) / 1024 / 1024,
				DiskTotalMB:   vm.MaxDisk / 1024 / 1024,
				DiskUsedMB:    vm.Disk / 1024 / 1024,
				Uptime:        vm.Uptime,
			}

			if vmStats.MemoryTotalMB > 0 {
				vmStats.MemoryPercent = uint8(math.Min(float64(vmStats.MemoryUsedMB)/float64(vmStats.MemoryTotalMB)*100, 100))
			}
			if vmStats.DiskTotalMB > 0 {
				vmStats.DiskPercent = uint8(math.Min(float64(vmStats.DiskUsedMB)/float64(vmStats.DiskTotalMB)*100, 100))
			}

			// 格式化运行时长
			uptime := time.Duration(vmStats.Uptime) * time.Second
			days := int(uptime.Hours() / 24)
			hours := int(uptime.Hours()) % 24
			minutes := int(uptime.Minutes()) % 60

			if days > 0 {
				vmStats.UptimeStr = fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
			} else if hours > 0 {
				vmStats.UptimeStr = fmt.Sprintf("%dh %dm", hours, minutes)
			} else {
				vmStats.UptimeStr = fmt.Sprintf("%dm", minutes)
			}

			node.VMs = append(node.VMs, vmStats)
		}

		// 按VMID排序虚拟机列表
		sort.Slice(node.VMs, func(i, j int) bool {
			return node.VMs[i].VMID < node.VMs[j].VMID
		})

		nodes = append(nodes, node)
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	widget.Nodes = nodes
}

func (widget *proxmoxWidget) Render() template.HTML {
	return widget.renderTemplate(widget, proxmoxStatsWidgetTemplate)
}

func fetchProxmoxClusterResources(w *proxmoxWidget) ([]proxmoxClusterResource, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	request, _ := http.NewRequestWithContext(ctx, "GET", w.URL+"/api2/json/cluster/resources", nil)
	request.Header.Set("Authorization", "PVEAPIToken="+w.Token+"="+w.Secret)

	var client = ternary(w.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
	result, err := decodeJsonFromRequest[multipleResponse[proxmoxClusterResource]](client, request)
	if err != nil {
		return nil, err
	}

	return result.Data, nil
}

func fetchProxmoxNodeStatus(w *proxmoxWidget, node string) (*proxmoxNodeStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	request, _ := http.NewRequestWithContext(ctx, "GET", w.URL+"/api2/json/nodes/"+node+"/status", nil)
	request.Header.Set("Authorization", "PVEAPIToken="+w.Token+"="+w.Secret)

	var client = ternary(w.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
	var result singleResponse[proxmoxNodeStatus]

	result, err := decodeJsonFromRequest[singleResponse[proxmoxNodeStatus]](client, request)
	if err != nil {
		return nil, err
	}

	return &result.Data, nil
}
