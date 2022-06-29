package obs_clone

import (
	"fmt"
	"github.com/opensourceways/code-server-operator/controllers/initplugins/interface"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"path/filepath"
)

const (
	OBSCloneImageUrl = "opensourceway/xihe-plugin:0.1.2"
)

type OBSClonePlugin struct {
	Client        _interface.PluginClients
	Parameters    []string
	ImageUrl      string
	BaseDirectory string
}

func (p *OBSClonePlugin) parseFlags(arguments []string) {
	p.Parameters = arguments
	p.ImageUrl = OBSCloneImageUrl
	return
}

func Create(c _interface.PluginClients, parameters []string, baseDir string) _interface.PluginInterface {
	gitPlugin := OBSClonePlugin{Client: c, BaseDirectory: baseDir}
	gitPlugin.parseFlags(parameters)
	return &gitPlugin
}

func (p *OBSClonePlugin) GenerateInitContainerSpec() *corev1.Container {
	//TODO update this logic
	args := append(p.Parameters, fmt.Sprintf("--dest=%s", filepath.Join(p.BaseDirectory, "content")))
	container := corev1.Container{
		Image:           p.ImageUrl,
		Name:            "init-obs-clone",
		ImagePullPolicy: corev1.PullIfNotPresent,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("0.5"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("0.5"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
		Args: args,
	}
	return &container
}

func (p *OBSClonePlugin) SetDefaultImage(imageUrl string) {
	p.ImageUrl = imageUrl
}

func (p *OBSClonePlugin) SetWorkingDir(baseDir string) {
	p.BaseDirectory = baseDir
}

func GetName() string {
	return "obs-clone"
}
