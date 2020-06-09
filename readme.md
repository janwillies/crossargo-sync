# Crossplane ArgoCD Sync
This is a Kubernetes controller which watches for newly created EKS clusters and configures ArgoCD to provision software to it via gitops.

When a new EKS Cluster is created in Crossplane, this controller creates the following:
1. a Kubernetes cluster in ArgoCD
2. an `AppProject` in ArgoCD which references the cluster 
3. an `Application` in this project which references a git repo

All manifests in the git repo are automatically deployed to the EKS cluster

## build and deploy the controller
Use [ko](https://github.com/google/ko) to build and deploy:
```bash
export KO_DOCKER_REPO=foobar.dkr.ecr.eu-west-1.amazonaws.com/crossargo-sync
ko apply -f deployment.yaml
```