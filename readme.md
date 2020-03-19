# Crossplane ArgoCD Sync
Creates a Cluster in ArgoCD when a new Kubernetes Cluster is deployed via crossplane

## build and deploy the controller
Use [ko](https://github.com/google/ko) to build and deploy:
```bash
export KO_DOCKER_REPO=foobar.dkr.ecr.eu-west-1.amazonaws.com/crossargo-sync
ko apply -f deployment.yaml
```