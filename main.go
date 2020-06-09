package main

import (
	b64 "encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	argo_v1alpha1 "github.com/argoproj/argo-cd/pkg/apis/application/v1alpha1"
	"github.com/jamiealquiza/envy"

	// informers "github.com/argoproj/argo-cd/pkg/client/informers/externalversions"
	argo_clientset "github.com/argoproj/argo-cd/pkg/client/clientset/versioned"

	// clientset_argo "github.com/argoproj/argo-cd/pkg/client/clientset/versioned"
	// informers_argo "github.com/argoproj/argo-cd/pkg/client/informers/externalversions"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeinformers "k8s.io/client-go/informers"
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

	// parse commandline flags
	var region = flag.String("region", "eu-west-1", "AWS Region")
	var awsAccountID = flag.String("awsaccountid", "018708700358", "AWS account ID (12-digit number)")
	envy.Parse("ARGOCROSS")
	flag.Parse()

	// connect to Kubernetes API
	kubeconfig := os.Getenv("KUBECONFIG")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// set api clients up
	// kubernetes core api
	clientsetCore, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	// argo crd api
	clientsetArgo, err := argo_clientset.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	// listen for new secrets
	factory := kubeinformers.NewSharedInformerFactoryWithOptions(clientsetCore, 0, kubeinformers.WithNamespace(namespace()))
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

					// clustername needs to be in this specific format to be accepted by argo
					// (actually not sure about it, read a comment on github)
					var argoClusterName string = "arn:aws:eks:" + *region + ":" + *awsAccountID + ":cluster/" + argoEksConfig.AwsAuthConfig.ClusterName
					// argoClusterName := argoEksConfig.AwsAuthConfig.ClusterName

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
							"name":   []byte(argoClusterName),
							"server": []byte(server),
						},
						Type: "Opaque",
					}

					secretOut, err := clientsetCore.CoreV1().Secrets("argocd").Create(&secret)
					if err != nil {
						fmt.Println(err)
					} else {
						fmt.Println("Added cluster", secretOut.GetName())
					}

					// initial argo project
					argoProject := argo_v1alpha1.AppProject{
						TypeMeta: metav1.TypeMeta{
							Kind:       "AppProject",
							APIVersion: "argoproj.io/v1alpha1",
						},
						ObjectMeta: metav1.ObjectMeta{
							Name: namespace() + "-" + argoEksConfig.AwsAuthConfig.ClusterName,
						},
						Spec: argo_v1alpha1.AppProjectSpec{
							Description: argoEksConfig.AwsAuthConfig.ClusterName + " EKS cluster owned by " + namespace(),
							Destinations: []argo_v1alpha1.ApplicationDestination{
								argo_v1alpha1.ApplicationDestination{
									Namespace: "istio-system",
									Server:    server,
								},
								argo_v1alpha1.ApplicationDestination{
									Namespace: "istio-operator",
									Server:    server,
								},
								argo_v1alpha1.ApplicationDestination{
									Namespace: "styra-system",
									Server:    server,
								},
								argo_v1alpha1.ApplicationDestination{
									Namespace: "knative-serving",
									Server:    server,
								},
								argo_v1alpha1.ApplicationDestination{
									Namespace: "serving-operator",
									Server:    server,
								},
							},
							ClusterResourceWhitelist: []metav1.GroupKind{
								metav1.GroupKind{
									Group: "*",
									Kind:  "*",
								},
							},
							SourceRepos: []string{"https://github.com/janwillies/gitops-manifests-private"},
							// OrphanedResources: &argo_v1alpha1.OrphanedResourcesMonitorSettings{},
						},
					}
					argoProjectOut, err := clientsetArgo.ArgoprojV1alpha1().AppProjects("argocd").Create(&argoProject)
					if err != nil {
						fmt.Println(err)
					} else {
						fmt.Println("Added project", argoProjectOut.GetName())
					}

					// intial argo application
					argoApplication := argo_v1alpha1.Application{
						TypeMeta: metav1.TypeMeta{
							// Kind:       argo_v1alpha1.ApplicationSchemaGroupVersionKind.String(),
							// APIVersion: argo_v1alpha1.AppProjectSchemaGroupVersionKind.GroupVersion().Identifier(),
							Kind:       "Application",
							APIVersion: "argoproj.io/v1alpha1",
						},
						ObjectMeta: metav1.ObjectMeta{
							Name: "infra-" + namespace() + "-" + argoEksConfig.AwsAuthConfig.ClusterName,
							// Finalizers: []string{"resources-finalizer.argocd.argoproj.io"},
						},
						Spec: argo_v1alpha1.ApplicationSpec{
							Project: namespace() + "-" + argoEksConfig.AwsAuthConfig.ClusterName,
							Destination: argo_v1alpha1.ApplicationDestination{
								Namespace: "styra-system",
								Server:    server,
							},
							// Source: argo_v1alpha1.ApplicationSource{
							// 	RepoURL:        "https://github.com/janwillies/gitops-manifests-private",
							// 	Path:           "user-infra",
							// 	TargetRevision: "HEAD",
							// },
							Source: argo_v1alpha1.ApplicationSource{
								RepoURL:        "https://github.com/janwillies/gitops-manifests-private",
								Path:           "user-infra",
								TargetRevision: "HEAD",
							},
							SyncPolicy: &argo_v1alpha1.SyncPolicy{
								Automated: &argo_v1alpha1.SyncPolicyAutomated{
									Prune:    true,
									SelfHeal: true,
								},
							},
						},
					}
					argoApplicationOut, err := clientsetArgo.ArgoprojV1alpha1().Applications("argocd").Create(&argoApplication)
					if err != nil {
						fmt.Println(err)
					} else {
						fmt.Println("Added application", argoApplicationOut.GetName())
					}

				}
			}

		},
		// TODO: Implement update function
		// UpdateFunc: func(old interface{}, new interface{}) {
		// 	var secret = new.(*v1.Secret).DeepCopy()
		// },
		DeleteFunc: func(obj interface{}) {
			// get the secret
			var secret = obj.(*v1.Secret).DeepCopy()

			// check if the owner is of Kind: KubernetesCluster
			// to make sure it's a crossplane kubernetes secret
			for _, o := range secret.OwnerReferences {
				if o.Kind == "KubernetesCluster" {

					// prepare argo config
					var clusterName string

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
							clusterName = kubeConfig.CurrentContext
						}
					}

					err = clientsetArgo.ArgoprojV1alpha1().Applications("argocd").Delete("infra-"+namespace()+"-"+clusterName, &metav1.DeleteOptions{})
					if err != nil {
						fmt.Println(err)
					}
					fmt.Println("Deleted application", "infra-"+namespace()+"-"+clusterName)

					err = clientsetArgo.ArgoprojV1alpha1().AppProjects("argocd").Delete(namespace()+"-"+clusterName, &metav1.DeleteOptions{})
					if err != nil {
						fmt.Println(err)
					}
					fmt.Println("Deleted project", namespace()+"-"+clusterName)

					err = clientsetCore.CoreV1().Secrets("argocd").Delete(namespace()+"-"+clusterName, &metav1.DeleteOptions{})
					if err != nil {
						fmt.Println(err)
					}
					fmt.Println("Deleted cluster", namespace()+"-"+clusterName)
				}
			}
		},
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

// type ArgoProj struct {
// 	APIVersion string `json:"apiVersion"`
// 	Kind       string `json:"kind"`
// 	Metadata   struct {
// 		Name      string `json:"name"`
// 		Namespace string `json:"namespace"`
// 	} `json:"metadata"`
// 	Spec struct {
// 		ClusterResourceWhitelist []struct {
// 			Group string `json:"group"`
// 			Kind  string `json:"kind"`
// 		} `json:"clusterResourceWhitelist"`
// 		Description  string `json:"description"`
// 		Destinations []struct {
// 			Namespace string `json:"namespace"`
// 			Server    string `json:"server"`
// 		} `json:"destinations"`
// 		SourceRepos []string `json:"sourceRepos"`
// 	} `json:"spec"`
// }
