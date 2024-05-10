package v1beta1

func NewService() *Service {
	return &Service{}
}

func NewIngress() *Ingress {
	return &Ingress{}
}

func NewIngressManagerSpec() *IngressManagerSpec {
	return &IngressManagerSpec{
		Service: NewService(),
		Ingress: NewIngress(),
	}
}

func NewIngressManagerStatus() *IngressManagerStatus {
	return &IngressManagerStatus{}
}

func NewIngressManager() *IngressManager {
	return &IngressManager{
		Spec:   *NewIngressManagerSpec(),
		Status: *NewIngressManagerStatus(),
	}
}

func NewIngressManagerList() *IngressManagerList {
	return &IngressManagerList{
		Items: make([]IngressManager, 0),
	}
}

func (i *IngressManagerList) AddItems(items ...IngressManager) {
	i.Items = append(i.Items, items...)
}
