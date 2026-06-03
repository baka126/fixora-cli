import re

with open("internal/analyzer/precision.go", "r") as f:
    content = f.read()

# Change signatures
content = content.replace("func (a Analyzer) runPrecisionAnalyzers(ctx context.Context)", "func (a Analyzer) runPrecisionAnalyzers(ctx *ScanContext)")
content = content.replace("run     func(context.Context) ([]Finding, error)", "run     func(*ScanContext) ([]Finding, error)")

content = content.replace("func (a Analyzer) analyzeServiceEndpoints(ctx context.Context)", "func (a Analyzer) analyzeServiceEndpoints(ctx *ScanContext)")
content = content.replace("func (a Analyzer) analyzeIngressBackends(ctx context.Context)", "func (a Analyzer) analyzeIngressBackends(ctx *ScanContext)")
content = content.replace("func (a Analyzer) analyzeHPATargets(ctx context.Context)", "func (a Analyzer) analyzeHPATargets(ctx *ScanContext)")
content = content.replace("func (a Analyzer) analyzePodSecurity(ctx context.Context)", "func (a Analyzer) analyzePodSecurity(ctx *ScanContext)")
content = content.replace("func (a Analyzer) analyzePDBs(ctx context.Context)", "func (a Analyzer) analyzePDBs(ctx *ScanContext)")
content = content.replace("func (a Analyzer) analyzeWebhooks(ctx context.Context)", "func (a Analyzer) analyzeWebhooks(ctx *ScanContext)")
content = content.replace("func (a Analyzer) analyzeGatewayAPI(ctx context.Context)", "func (a Analyzer) analyzeGatewayAPI(ctx *ScanContext)")
content = content.replace("func (a Analyzer) analyzeRBAC(ctx context.Context)", "func (a Analyzer) analyzeRBAC(ctx *ScanContext)")
content = content.replace("func (a Analyzer) analyzeStorage(ctx context.Context)", "func (a Analyzer) analyzeStorage(ctx *ScanContext)")
content = content.replace("func (a Analyzer) objectNameExists(ctx context.Context,", "func (a Analyzer) objectNameExists(ctx *ScanContext,")

# Replace a.k.GetResourceItems(ctx, a.opts.Namespace, a.opts.AllNS, ...) with ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, ...)
content = content.replace("a.k.GetResourceItems(ctx, a.opts.Namespace, a.opts.AllNS, ", "ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, ")
content = content.replace('a.k.GetResourceItems(ctx, "", true, ', 'ctx.GetResourceItems("", true, ')

# Replace a.k.GetResource(ctx, namespace, targetResource) with ctx.GetResource(namespace, targetResource)
content = content.replace("a.k.GetResource(ctx, namespace, targetResource)", "ctx.GetResource(namespace, targetResource)")

# Replace a.k.Run(ctx, args...) with a.k.Run(ctx.Context, args...)
content = content.replace("a.k.Run(ctx, args...)", "ctx.Reader.Run(ctx.Context, args...)")

with open("internal/analyzer/precision.go", "w") as f:
    f.write(content)
