#!/usr/bin/env bash
# ============================================================================
# deployment.sh — IsItObservable Sympozium episode (ISI-1386)
# ----------------------------------------------------------------------------
# Deploys the full stack end-to-end onto the workload cluster:
#   1. Platform pieces   : MetalLB, csi-driver-nfs (+ default StorageClass),
#                          cert-manager, (optional) kgateway / Gateway API
#   2. Control plane      : Sympozium via Helm (charts/sympozium)
#   3. Skills             : the custom `bmad` SkillPack
#   4. Secrets            : Ollama placeholder + Slack tokens
#   5. Ensemble           : the `bmad-ensemble` PersonaPack (coding + review +
#                          tech-lead) on local Ollama models served by the
#                          macstudio, with the macstudio baseURL patched onto
#                          each generated SympoziumInstance
#   6. Verification       : wait for rollouts and print the resulting topology
#
# This script is BLOCKED on the cluster being up (sibling ProxOps issue
# ISI-1385: CNI/MetalLB/csi-driver-nfs). Once the cluster is reachable with a
# working kubeconfig, run it. CNI itself is a cluster-bootstrap concern owned by
# ProxOps; this script verifies it and applies the remaining platform pieces.
#
# Usage:
#   export MACSTUDIO_IP=10.0.0.50            # Ollama host (REQUIRED)
#   export METALLB_IP_RANGE=10.0.0.240-10.0.0.250
#   export NFS_SERVER=10.0.0.10
#   export NFS_SHARE=/export/sympozium
#   export SLACK_BOT_TOKEN=xoxb-...          # optional (Slack channel)
#   export SLACK_APP_TOKEN=xapp-...          # optional (Socket Mode)
#   ./deployment.sh                          # full deploy
#   ./deployment.sh --skip-platform          # cluster pieces already provisioned
#   ./deployment.sh --skip-ensemble          # control plane only
#   INSTALL_KGATEWAY=true ./deployment.sh    # also install kgateway + Gateway API
# ============================================================================
set -euo pipefail

# --- pinned versions --------------------------------------------------------
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.17.1}"
METALLB_VERSION="${METALLB_VERSION:-v0.14.8}"
CSI_NFS_VERSION="${CSI_NFS_VERSION:-v4.9.0}"
GATEWAY_API_VERSION="${GATEWAY_API_VERSION:-v1.2.1}"
KGATEWAY_VERSION="${KGATEWAY_VERSION:-v2.0.0}"

# --- config -----------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EPISODE_DIR="${SCRIPT_DIR}/deploy/bmad-episode"
SYMPO_NS="${SYMPO_NS:-sympozium-system}"
PACK_NS="${PACK_NS:-default}"
HELM_RELEASE="${HELM_RELEASE:-sympozium}"
OLLAMA_PORT="${OLLAMA_PORT:-11434}"

SKIP_PLATFORM="false"
SKIP_ENSEMBLE="false"
INSTALL_KGATEWAY="${INSTALL_KGATEWAY:-false}"

# --- helpers ----------------------------------------------------------------
c_info()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
c_step()  { printf '\033[1;36m  ->\033[0m %s\n' "$*"; }
c_warn()  { printf '\033[1;33m  ! \033[0m %s\n' "$*" >&2; }
c_err()   { printf '\033[1;31m  ! \033[0m %s\n' "$*" >&2; exit 1; }
need()    { command -v "$1" >/dev/null 2>&1 || c_err "Required tool '$1' not found in PATH."; }

parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --skip-platform) SKIP_PLATFORM="true" ;;
            --skip-ensemble) SKIP_ENSEMBLE="true" ;;
            --with-kgateway) INSTALL_KGATEWAY="true" ;;
            -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
            *) c_warn "Unknown argument: $1" ;;
        esac
        shift
    done
}

# ============================================================================
# 0. Preflight
# ============================================================================
preflight() {
    c_info "Preflight checks"
    need kubectl; need helm; need envsubst
    kubectl cluster-info >/dev/null 2>&1 \
        || c_err "Cannot reach the cluster. Is the kubeconfig pointed at the rebuilt workload cluster (ISI-1385)?"
    c_step "Cluster reachable: $(kubectl config current-context)"

    # CNI sanity — owned by ProxOps (ISI-1385) but verified here.
    if ! kubectl get nodes 2>/dev/null | grep -q ' Ready '; then
        c_warn "No Ready nodes — CNI may not be up yet (ProxOps / ISI-1385)."
    else
        c_step "Nodes Ready: $(kubectl get nodes --no-headers 2>/dev/null | grep -c ' Ready ')"
    fi

    [ -n "${MACSTUDIO_IP:-}" ] || c_err "MACSTUDIO_IP is required (Ollama host serving the local models)."
    OLLAMA_BASE_URL="http://${MACSTUDIO_IP}:${OLLAMA_PORT}/v1"
    c_step "Local model endpoint (macstudio): ${OLLAMA_BASE_URL}"

    # Best-effort reachability probe of the two episode models.
    if command -v curl >/dev/null 2>&1; then
        if curl -fsS --max-time 5 "http://${MACSTUDIO_IP}:${OLLAMA_PORT}/api/tags" >/dev/null 2>&1; then
            c_step "macstudio Ollama reachable from this host."
        else
            c_warn "Could not reach Ollama at ${MACSTUDIO_IP}:${OLLAMA_PORT} from here (pods may still reach it). Ensure OLLAMA_HOST=0.0.0.0 and qwen3.6:latest + qwen3.5:122b are pulled."
        fi
    fi
}

# ============================================================================
# 1. Platform pieces
# ============================================================================
install_platform() {
    if [ "$SKIP_PLATFORM" = "true" ]; then
        c_info "Skipping platform pieces (--skip-platform)"; return
    fi
    c_info "Platform: MetalLB, csi-driver-nfs, cert-manager$([ "$INSTALL_KGATEWAY" = "true" ] && echo ', kgateway')"

    # --- MetalLB ---
    [ -n "${METALLB_IP_RANGE:-}" ] || c_err "METALLB_IP_RANGE is required (e.g. 10.0.0.240-10.0.0.250) unless --skip-platform."
    c_step "Installing MetalLB ${METALLB_VERSION}"
    kubectl apply -f "https://raw.githubusercontent.com/metallb/metallb/${METALLB_VERSION}/config/manifests/metallb-native.yaml"
    kubectl -n metallb-system rollout status deploy/controller --timeout=180s
    kubectl -n metallb-system rollout status daemonset/speaker --timeout=180s
    c_step "Applying MetalLB address pool: ${METALLB_IP_RANGE}"
    envsubst < "${EPISODE_DIR}/platform/metallb-pool.yaml.tmpl" | kubectl apply -f -

    # --- csi-driver-nfs + StorageClass ---
    [ -n "${NFS_SERVER:-}" ] && [ -n "${NFS_SHARE:-}" ] \
        || c_err "NFS_SERVER and NFS_SHARE are required (csi-driver-nfs StorageClass) unless --skip-platform."
    c_step "Installing csi-driver-nfs ${CSI_NFS_VERSION}"
    helm repo add csi-driver-nfs https://raw.githubusercontent.com/kubernetes-csi/csi-driver-nfs/master/charts >/dev/null 2>&1 || true
    helm repo update csi-driver-nfs >/dev/null
    helm upgrade --install csi-driver-nfs csi-driver-nfs/csi-driver-nfs \
        --namespace kube-system --version "${CSI_NFS_VERSION}" --wait
    c_step "Applying nfs-csi StorageClass (server=${NFS_SERVER} share=${NFS_SHARE})"
    envsubst < "${EPISODE_DIR}/platform/nfs-storageclass.yaml.tmpl" | kubectl apply -f -

    # --- cert-manager (Sympozium webhook TLS prereq) ---
    c_step "Installing cert-manager ${CERT_MANAGER_VERSION}"
    kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
    kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=180s

    # --- kgateway (optional Gateway API implementation) ---
    if [ "$INSTALL_KGATEWAY" = "true" ]; then
        c_step "Installing Gateway API CRDs ${GATEWAY_API_VERSION} + kgateway ${KGATEWAY_VERSION}"
        kubectl apply -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/standard-install.yaml"
        helm upgrade --install kgateway-crds "oci://cr.kgateway.dev/kgateway-dev/charts/kgateway-crds" \
            --version "${KGATEWAY_VERSION}" --namespace kgateway-system --create-namespace --wait
        helm upgrade --install kgateway "oci://cr.kgateway.dev/kgateway-dev/charts/kgateway" \
            --version "${KGATEWAY_VERSION}" --namespace kgateway-system --wait
    else
        c_step "kgateway install skipped (set INSTALL_KGATEWAY=true / --with-kgateway to enable)"
    fi
}

# ============================================================================
# 2. Sympozium control plane (Helm)
# ============================================================================
install_sympozium() {
    c_info "Sympozium control plane (Helm release: ${HELM_RELEASE})"
    helm upgrade --install "${HELM_RELEASE}" "${SCRIPT_DIR}/charts/sympozium" \
        --namespace "${SYMPO_NS}" --create-namespace \
        -f "${EPISODE_DIR}/values.yaml" --wait --timeout 8m
    kubectl -n "${SYMPO_NS}" rollout status deploy --selector app.kubernetes.io/instance="${HELM_RELEASE}" --timeout=300s 2>/dev/null || true
    c_step "Control plane installed."
}

# ============================================================================
# 3. Custom bmad SkillPack
# ============================================================================
install_skills() {
    c_info "Applying custom 'bmad' SkillPack"
    kubectl apply -f "${SCRIPT_DIR}/config/skills/bmad.yaml"
}

# ============================================================================
# 4. Secrets (Ollama placeholder + Slack)
# ============================================================================
install_secrets() {
    c_info "Provisioning secrets in namespace '${PACK_NS}'"

    # Ollama needs no key, but authRefs is mandatory -> placeholder secret.
    c_step "Ollama placeholder secret: bmad-ollama-key"
    kubectl -n "${PACK_NS}" create secret generic bmad-ollama-key \
        --from-literal=OPENAI_API_KEY=not-needed \
        --dry-run=client -o yaml | kubectl apply -f -

    # Slack tokens (Socket Mode). Both tokens recommended; app token enables
    # Socket Mode (no public webhook needed).
    if [ -n "${SLACK_BOT_TOKEN:-}" ]; then
        c_step "Slack secret: bmad-slack-tokens"
        kubectl -n "${PACK_NS}" create secret generic bmad-slack-tokens \
            --from-literal=SLACK_BOT_TOKEN="${SLACK_BOT_TOKEN}" \
            --from-literal=SLACK_APP_TOKEN="${SLACK_APP_TOKEN:-}" \
            --dry-run=client -o yaml | kubectl apply -f -
        [ -n "${SLACK_APP_TOKEN:-}" ] || c_warn "SLACK_APP_TOKEN empty — Slack will fall back to Events API mode (needs a public webhook)."
    else
        c_warn "SLACK_BOT_TOKEN not set — creating a placeholder Slack secret so the pack applies."
        c_warn "Set SLACK_BOT_TOKEN + SLACK_APP_TOKEN and re-run to enable Slack (see deploy-notes.md)."
        kubectl -n "${PACK_NS}" create secret generic bmad-slack-tokens \
            --from-literal=SLACK_BOT_TOKEN=REPLACE_ME \
            --from-literal=SLACK_APP_TOKEN=REPLACE_ME \
            --dry-run=client -o yaml | kubectl apply -f -
    fi
}

# ============================================================================
# 5. bmad-ensemble PersonaPack + macstudio baseURL patch
# ============================================================================
install_ensemble() {
    if [ "$SKIP_ENSEMBLE" = "true" ]; then
        c_info "Skipping ensemble (--skip-ensemble)"; return
    fi
    c_info "Activating the bmad-ensemble PersonaPack"
    kubectl apply -f "${EPISODE_DIR}/personapack-bmad-ensemble.yaml"

    # The PersonaPack controller stamps out one SympoziumInstance per persona
    # (named <pack>-<persona>) but does NOT carry baseURL. Patch the macstudio
    # Ollama endpoint onto each generated instance so the local models are used.
    c_step "Waiting for the controller to stamp out instances..."
    for persona in tech-lead coding code-review; do
        inst="bmad-ensemble-${persona}"
        for i in $(seq 1 30); do
            if kubectl -n "${PACK_NS}" get sympoziuminstance "${inst}" >/dev/null 2>&1; then break; fi
            sleep 2
        done
        if kubectl -n "${PACK_NS}" get sympoziuminstance "${inst}" >/dev/null 2>&1; then
            c_step "Patching ${inst} baseURL -> ${OLLAMA_BASE_URL}"
            kubectl -n "${PACK_NS}" patch sympoziuminstance "${inst}" --type=merge \
                -p "{\"spec\":{\"agents\":{\"default\":{\"baseURL\":\"${OLLAMA_BASE_URL}\"}}}}"
        else
            c_warn "Instance ${inst} not created yet — re-run after the PersonaPack reconciles, or patch baseURL manually."
        fi
    done
}

# ============================================================================
# 6. Verification
# ============================================================================
verify() {
    c_info "Verification"
    echo "--- PersonaPack ---";       kubectl -n "${PACK_NS}" get personapack bmad-ensemble 2>/dev/null || true
    echo "--- SympoziumInstances ---"; kubectl -n "${PACK_NS}" get sympoziuminstance -l sympozium.ai/persona-pack=bmad-ensemble 2>/dev/null || true
    echo "--- Schedules ---";          kubectl -n "${PACK_NS}" get sympoziumschedule 2>/dev/null | grep bmad-ensemble || true
    echo "--- Model mapping (baseURL + model per agent) ---"
    for persona in tech-lead coding code-review; do
        kubectl -n "${PACK_NS}" get sympoziuminstance "bmad-ensemble-${persona}" \
            -o jsonpath="{.metadata.name}{'\t'}{.spec.agents.default.model}{'\t'}{.spec.agents.default.baseURL}{'\n'}" 2>/dev/null || true
    done
    echo "--- Memory ConfigMaps (showcase) ---"
    kubectl -n "${PACK_NS}" get configmap 2>/dev/null | grep -E 'bmad-ensemble.*-memory' || c_warn "Memory ConfigMaps appear after the first run of each agent."
    echo "--- Channel status (Slack) ---"
    kubectl -n "${PACK_NS}" get sympoziuminstance -l sympozium.ai/persona-pack=bmad-ensemble \
        -o jsonpath="{range .items[*]}{.metadata.name}{': '}{.status.channels}{'\n'}{end}" 2>/dev/null || true
    echo
    c_info "Done. Inspect an agent's memory with:"
    echo "    kubectl -n ${PACK_NS} get configmap bmad-ensemble-coding-memory -o jsonpath='{.data.MEMORY\\.md}'"
    c_info "Tail the Slack channel deployment with:"
    echo "    kubectl -n ${PACK_NS} logs -l sympozium.ai/channel=slack -f"
}

# ============================================================================
main() {
    parse_args "$@"
    preflight
    install_platform
    install_sympozium
    install_skills
    install_secrets
    install_ensemble
    verify
}
main "$@"
