package analyzer

import "github.com/fixora/kubectl-fixora/internal/redact"

func RedactFindingForAI(f Finding) Finding {
	f.ID = redact.KubernetesText(f.ID)
	f.Namespace = redact.KubernetesText(f.Namespace)
	f.ResourceKind = redact.KubernetesText(f.ResourceKind)
	f.ResourceName = redact.KubernetesText(f.ResourceName)
	f.PodName = redact.KubernetesText(f.PodName)
	f.Status = redact.KubernetesText(f.Status)
	f.Category = redact.KubernetesText(f.Category)
	f.Summary = redact.KubernetesText(f.Summary)
	f.ChangeCorrelation = redact.KubernetesText(f.ChangeCorrelation)
	for i := range f.Evidence {
		f.Evidence[i].Label = redact.KubernetesText(f.Evidence[i].Label)
		f.Evidence[i].Value = redact.KubernetesText(f.Evidence[i].Value)
	}
	for i := range f.OwnerChain {
		f.OwnerChain[i] = redact.KubernetesText(f.OwnerChain[i])
	}
	for i := range f.RecentChanges {
		f.RecentChanges[i] = redact.KubernetesText(f.RecentChanges[i])
	}
	for i := range f.Recommendations {
		f.Recommendations[i].Title = redact.KubernetesText(f.Recommendations[i].Title)
		f.Recommendations[i].Description = redact.KubernetesText(f.Recommendations[i].Description)
		f.Recommendations[i].PatchType = redact.KubernetesText(f.Recommendations[i].PatchType)
	}
	for i := range f.Logs {
		f.Logs[i].Source = redact.KubernetesText(f.Logs[i].Source)
		f.Logs[i].Text = redact.KubernetesText(f.Logs[i].Text)
	}
	f.GitOps.ManagedBy = redact.KubernetesText(f.GitOps.ManagedBy)
	f.GitOps.HelmRelease = redact.KubernetesText(f.GitOps.HelmRelease)
	f.GitOps.HelmChart = redact.KubernetesText(f.GitOps.HelmChart)
	f.GitOps.FluxHint = redact.KubernetesText(f.GitOps.FluxHint)
	f.GitOps.ArgoHint = redact.KubernetesText(f.GitOps.ArgoHint)
	f.GitOps.TargetAdvice = redact.KubernetesText(f.GitOps.TargetAdvice)
	f.AI = nil
	return f
}
