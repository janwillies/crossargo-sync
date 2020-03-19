package main

import (
	b64 "encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

// ArgoEksConfig is the base64 encoded "config" inlined in the argo secret format:
// ---
// apiVersion: v1
// data:
//   config: ZXlKCSJITkRiR2xsY...    // <- this
//   name: YXKuOdF...
//   server: aHR0cHM6...
// kind: Secret
type ArgoEksConfig struct {
	TLSClientConfig TLSClientConfig `json:"tlsClientConfig"`
	AwsAuthConfig   AwsAuthConfig   `json:"awsAuthConfig"`
}

type TLSClientConfig struct {
	Insecure bool   `json:"insecure"`
	CaData   string `json:"caData"`
}

type AwsAuthConfig struct {
	ClusterName string `json:"clusterName"`
}

func main() {
	kubeconfig := os.Getenv("KUBECONFIG")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	// listen for new secrets
	factory := informers.NewSharedInformerFactoryWithOptions(clientset, 0, informers.WithNamespace(namespace()))
	informer := factory.Core().V1().Secrets().Informer()
	stopper := make(chan struct{})
	defer close(stopper)

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(new interface{}) {
			// get the secret
			var secret = new.(*v1.Secret).DeepCopy()

			// check if the owner is of Kind: KubernetesCluster
			// to make sure it's a crossplane kubernetes secret
			for _, o := range secret.OwnerReferences {
				if o.Kind == "KubernetesCluster" {

					// prepare argo config
					argoEksConfig := ArgoEksConfig{}
					var server string

					// extract data from crossplane secret
					var data = *&secret.Data
					for k, v := range data {
						switch k {
						case "kubeconfig":
							var kubeConfig KubeConfig
							err := yaml.Unmarshal(v, &kubeConfig)
							if err != nil {
								fmt.Println(err)
								return
							}
							// The context is named after the aws eks clustername
							argoEksConfig.AwsAuthConfig.ClusterName = kubeConfig.CurrentContext
						case "clusterCA":
							b64 := b64.StdEncoding.EncodeToString(v)
							argoEksConfig.TLSClientConfig.CaData = b64
							argoEksConfig.TLSClientConfig.Insecure = false
						case "endpoint":
							server = string(v)
						}
					}
					argoEksConfigJSON, err := json.Marshal(argoEksConfig)
					if err != nil {
						fmt.Println(err)
						return
					}

					// write kubernetes secret to argocd namespace
					// (so that argocd picks it up as a cluster)
					secret := v1.Secret{
						TypeMeta: metav1.TypeMeta{
							Kind:       "Secret",
							APIVersion: "v1",
						},
						ObjectMeta: metav1.ObjectMeta{
							Name:      namespace() + "-" + argoEksConfig.AwsAuthConfig.ClusterName,
							Namespace: "argocd",
							Annotations: map[string]string{
								"managed-by": "argocd.argoproj.io",
							},
							Labels: map[string]string{
								"argocd.argoproj.io/secret-type": "cluster",
							},
						},
						Data: map[string][]byte{
							"config": []byte(argoEksConfigJSON),
							"name":   []byte("arn:aws:eks:eu-west-1:f00bar:cluster/" + argoEksConfig.AwsAuthConfig.ClusterName),
							"server": []byte(server),
						},
						Type: "Opaque",
					}

					secretOut, err := clientset.CoreV1().Secrets("argocd").Create(&secret)
					if err != nil {
						fmt.Println(err)
					} else {
						fmt.Println("Added cluster", secretOut.GetName())
					}
				}
			}

		},
		// TODO: Implement update function
		// UpdateFunc: func(old interface{}, new interface{}) {
		// 	var secret = new.(*v1.Secret).DeepCopy()

		// 	var data = *&secret.Data
		// 	for k, v := range data {
		// 		switch k {
		// 		case kubeconfig:
		// 			fmt.Println(v)
		// 		default:
		// 			fmt.Println("Kein Secret")
		// 		}
		// 	}

		// },
	})

	informer.Run(stopper)
}

// get current namespace
func namespace() string {
	// This way assumes you've set the POD_NAMESPACE environment variable using the downward API.
	// This check has to be done first for backwards compatibility with the way InClusterConfig was originally set up
	if ns, ok := os.LookupEnv("POD_NAMESPACE"); ok {
		return ns
	}

	// Fall back to the namespace associated with the service account token, if available
	if data, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); len(ns) > 0 {
			return ns
		}
	}

	return "default"
}

// KubeConfig holds data from crossplane secret field "kubeconfig"
type KubeConfig struct {
	APIVersion string `json:"apiVersion"`
	Clusters   []struct {
		Cluster struct {
			CertificateAuthorityData string `json:"certificate-authority-data"`
			Server                   string `json:"server"`
		} `json:"cluster"`
		Name string `json:"name"`
	} `json:"clusters"`
	Contexts []struct {
		Context struct {
			Cluster string `json:"cluster"`
			User    string `json:"user"`
		} `json:"context"`
		Name string `json:"name"`
	} `json:"contexts"`
	CurrentContext string `json:"current-context"`
	Kind           string `json:"kind"`
	Preferences    struct {
	} `json:"preferences"`
	Users []struct {
		Name string `json:"name"`
		User struct {
			Token string `json:"token"`
		} `json:"user"`
	} `json:"users"`
}
