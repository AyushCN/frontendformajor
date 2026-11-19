import { useState, useEffect } from "react"
import useSWR from "swr"
import toast from "react-hot-toast"
import {
  Loader2,
  Settings,
  Save,
  Trash2,
  Plus,
  Moon,
  Clock,
  ShieldAlert,
  CheckCircle2,
  AlertTriangle,
  ShieldCheck,
  HardDrive,
  Calendar,
  Hourglass,
  Box,
} from "lucide-react"
import { fetchWithAuth } from "@/lib/auth"
import EnvironmentVariablesManager from "@/components/EnvironmentVariablesManager"

export default function EnvironmentSettings({ env, mutate, isViewerRole }: { env: Record<string, unknown>, mutate: () => void, isViewerRole: boolean }) {
  const [sandboxName, setSandboxName] = useState((env.name as string) || (env.subdomain as string) || "")
  const [startCommand, setStartCommand] = useState((env.startCommand as string) || "")
  const [rootDirectory, setRootDirectory] = useState((env.rootDirectory as string) || "")
  const [dockerfilePath, setDockerfilePath] = useState((env.dockerfilePath as string) || "")
  const [preDeployCommand, setPreDeployCommand] = useState((env.preDeployCommand as string) || "")
  const [runtimeType, setRuntimeType] = useState((env.runtimeType as string) || "")
  const [port, setPort] = useState((env.port as number)?.toString() || "")
  const [healthCheckType, setHealthCheckType] = useState((env.healthCheckType as string) || "http")
  const [envVars, setEnvVars] = useState<{key: string, value: string}[]>(() => {
    if (!env.envVars || !Array.isArray(env.envVars)) return []
    return env.envVars.map((v: any) => ({ key: v.key, value: v.value }))
  })
  const [isSaving, setIsSaving] = useState(false)

  // Real-time lifecycle polling for authoritative UTC timestamps
  const { data: lifecycle, mutate: mutateLifecycle } = useSWR<any>(
    env?.id ? `/api/environments/${env.id}/lifecycle/status` : null,
    fetchWithAuth,
    { refreshInterval: 1000 }
  )

  const status = (lifecycle?.status || (env.status as string) || "IDLE").toUpperCase()
  const idleTimeoutSec = Number(lifecycle?.idle_timeout_seconds || env.idle_timeout_seconds || env.idleTimeoutSeconds || 900)
  const hibernatedExpirySec = Number(lifecycle?.hibernated_expiration_seconds || (env as any).hibernated_expiration_seconds || 7200)
  const workspaceRetentionSec = Number(lifecycle?.workspace_retention_seconds || (env as any).workspace_retention_seconds || 604800)

  const [idleTimeout, setIdleTimeout] = useState(idleTimeoutSec.toString())
  const [hibernatedExpiration, setHibernatedExpiration] = useState(hibernatedExpirySec.toString())
  const [workspaceRetention, setWorkspaceRetention] = useState(workspaceRetentionSec.toString())
  const [now, setNow] = useState(Date.now())

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])

  // Compute live hibernation countdown
  let hibernationText = "Active"
  if (status === "HIBERNATED") {
    hibernationText = "Paused (Hibernated)"
  } else if (status === "WAKING") {
    hibernationText = "Waking Up..."
  } else if (status === "EXPIRED" || status === "DELETED") {
    hibernationText = "Not applicable"
  } else if (status === "RUNNING") {
    const rem = lifecycle?.idle_ttl_remaining_sec !== undefined ? lifecycle.idle_ttl_remaining_sec : idleTimeoutSec
    const mins = Math.floor(Math.max(0, rem) / 60)
    const secs = Math.max(0, rem) % 60
    hibernationText = `${mins}m ${secs.toString().padStart(2, "0")}s`
  } else {
    hibernationText = status
  }

  // Compute live hibernated expiration countdown
  let expirationCountdown = "Not applicable"
  let absoluteExpiration = "Not applicable"
  if (status === "HIBERNATED") {
    const expiresAtStr = lifecycle?.expires_at || (env as any).expires_at
    if (expiresAtStr && new Date(expiresAtStr).getTime() > 0) {
      const targetExpire = new Date(expiresAtStr).getTime()
      const rem = Math.max(0, Math.floor((targetExpire - now) / 1000))
      if (rem <= 0) {
        expirationCountdown = "Expired"
      } else {
        const hours = Math.floor(rem / 3600)
        const mins = Math.floor((rem % 3600) / 60)
        const secs = rem % 60
        expirationCountdown = `${hours}h ${mins.toString().padStart(2, "0")}m ${secs.toString().padStart(2, "0")}s`
      }
      absoluteExpiration = new Date(expiresAtStr).toUTCString()
    } else {
      expirationCountdown = "Calculating..."
    }
  } else if (status === "EXPIRED" || status === "DELETED") {
    expirationCountdown = "Expired (Container Torn Down)"
    absoluteExpiration = "Expired"
  } else {
    // RUNNING or WAKING
    expirationCountdown = "Not applicable"
    absoluteExpiration = "Not applicable (Inactive while Running)"
  }

  // Compute data retention deadline
  let retentionText = "7 days post-expiration"
  let retentionDeadline = "Scheduled after expiration"
  const retentionExpiresAtStr = lifecycle?.retention_expires_at
  if (status === "EXPIRED" && retentionExpiresAtStr) {
    const targetRetention = new Date(retentionExpiresAtStr).getTime()
    const rem = Math.max(0, Math.floor((targetRetention - now) / 1000))
    const days = Math.floor(rem / 86400)
    const hours = Math.floor((rem % 86400) / 3600)
    retentionText = `${days}d ${hours}h remaining`
    retentionDeadline = new Date(retentionExpiresAtStr).toUTCString()
  } else {
    const days = Math.round(parseInt(workspaceRetention) / 86400)
    retentionText = `${days} days post-expiration`
    retentionDeadline = `Retained on disk for ${days} days after container expiration`
  }

  const handleSave = async () => {
    setIsSaving(true)
    try {
      const res = await fetch(`/api/environments/${env.id}/settings`, {
        method: "PUT",
        headers: {
          "Authorization": `Bearer ${localStorage.getItem("token")}`,
          "Content-Type": "application/json"
        },
        body: JSON.stringify({
          name: sandboxName.trim() || null,
          startCommand: startCommand || null,
          rootDirectory: rootDirectory || null,
          dockerfilePath: dockerfilePath || null,
          preDeployCommand: preDeployCommand || null,
          runtimeType: runtimeType || null,
          port: port ? parseInt(port) : null,
          healthCheckType,
          idleTimeoutSeconds: parseInt(idleTimeout),
          hibernatedExpirationSeconds: parseInt(hibernatedExpiration),
          workspaceRetentionSeconds: parseInt(workspaceRetention),
          envVars: envVars.reduce((acc, curr) => {
            if (curr.key.trim()) acc[curr.key.trim()] = curr.value
            return acc
          }, {} as Record<string, string>)
        })
      })
      const data = await res.json()
      if (res.ok) {
        toast.success(data.message || "Settings updated!")
        mutate()
        mutateLifecycle()
      } else {
        throw new Error(data.error || "Failed to update settings")
      }
    } catch(err) {
      if (err instanceof Error) {
        toast.error((err as Error).message)
      } else {
        toast.error("An unknown error occurred")
      }
    } finally {
      setIsSaving(false)
    }
  }

  return (
    <div
      className="w-full bg-surface-container-lowest border border-outline-variant rounded-xl overflow-hidden flex flex-col"
      style={{ height: "calc(100vh - 260px)", minHeight: "520px" }}
    >
      {/* Header & Action Bar */}
      <div className="bg-surface-container/60 border-b border-outline-variant px-5 py-3.5 flex flex-wrap items-center justify-between gap-3 shrink-0">
        <div>
          <div className="flex items-center gap-2">
            <Settings className="w-4 h-4 text-primary-fixed" />
            <h3 className="font-semibold text-on-surface text-sm">
              Environment Settings & Lifecycle
            </h3>
          </div>
          <p className="text-[11px] text-on-surface-variant/70 mt-0.5">
            Configure server-enforced lifecycle policies, runtime build options, and encrypted secrets.
          </p>
        </div>

        {!isViewerRole && (
          <div className="flex items-center gap-2">
            <button
              onClick={handleSave}
              disabled={isSaving}
              className="flex items-center gap-1.5 px-3.5 py-1.5 rounded-lg bg-primary-fixed text-on-primary-fixed hover:opacity-90 transition-opacity text-xs font-semibold shadow-sm cursor-pointer disabled:opacity-50"
            >
              {isSaving ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Save className="w-3.5 h-3.5" />}
              Save Settings
            </button>
          </div>
        )}
      </div>

      {/* Scrollable Main Area */}
      <div className="flex-1 w-full bg-surface-container-lowest overflow-y-auto p-6 space-y-6">
        {/* Sandbox Identity & Routing Card */}
        <div className="border border-outline-variant/60 rounded-xl bg-surface-container/30 p-5 space-y-4">
          <div className="pb-3 border-b border-outline-variant/40 flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Box className="w-4 h-4 text-primary-fixed" />
              <div>
                <h4 className="text-sm font-bold text-on-surface">Sandbox Identity & Routing</h4>
                <p className="text-xs text-on-surface-variant/70">Canonical display name, URL slug, and preview domain routing.</p>
              </div>
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <label className="block text-xs font-semibold text-on-surface-variant mb-1">
                Sandbox Display Name
              </label>
              <input
                type="text"
                value={sandboxName}
                onChange={(e) => setSandboxName(e.target.value)}
                disabled={isViewerRole}
                placeholder="e.g. Weather API Development"
                className="w-full px-3 py-2 text-xs bg-surface-container/60 border border-outline-variant rounded-lg focus:border-primary-fixed focus:ring-1 focus:ring-primary-fixed outline-none transition-all disabled:opacity-50 text-on-surface font-medium"
              />
              <p className="text-[10px] text-on-surface-variant/60 mt-1">
                Canonical human-readable name shown across dashboards, headers, and team activity.
              </p>
            </div>

            <div>
              <label className="block text-xs font-semibold text-on-surface-variant mb-1">
                Canonical Slug & Subdomain
              </label>
              <div className="px-3 py-2 text-xs bg-surface-container-high/60 border border-outline-variant rounded-lg font-mono text-on-surface select-all truncate">
                {(env.slug as string) || (env.subdomain as string) || "auto-generated"}
              </div>
              <p className="text-[10px] text-on-surface-variant/60 mt-1">
                Hostname: <span className="font-mono text-primary-fixed">{(env.subdomain as string) || "..."}.127.0.0.1.nip.io</span>
              </p>
            </div>
          </div>
        </div>

        {/* Sandbox Lifecycle & Security Policy Card */}
        <div className="border border-outline-variant/60 rounded-xl bg-surface-container/30 p-5 space-y-5">
          <div className="flex items-center justify-between pb-3 border-b border-outline-variant/40">
            <div className="flex items-center gap-2">
              <Clock className="w-5 h-5 text-primary-fixed" />
              <div>
                <h4 className="text-sm font-bold text-on-surface">Lifecycle & Security Policy</h4>
                <p className="text-xs text-on-surface-variant/70">Server-enforced idle timeouts, auto-hibernation, and maximum lifetime caps.</p>
              </div>
            </div>
            <span className="text-[11px] font-mono px-2.5 py-1 rounded-full border border-outline-variant bg-surface-container-high text-on-surface-variant font-semibold">
              Status: {status}
            </span>
          </div>

          {/* Authoritative Resettable Lifecycle Cards */}
          <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
            {/* Card 1: Hibernation */}
            <div className="p-4 rounded-xl bg-surface-container/50 border border-outline-variant/50 flex flex-col justify-between space-y-3">
              <div>
                <div className="flex items-center justify-between text-xs text-on-surface-variant mb-1">
                  <span className="flex items-center gap-1.5 font-semibold text-amber-500">
                    <Moon className="w-4 h-4" />
                    Hibernation
                  </span>
                  <span className="text-[10px] uppercase font-bold tracking-wider px-2 py-0.5 rounded bg-amber-500/10 text-amber-500 border border-amber-500/20">
                    {status === "HIBERNATED" ? "Hibernated" : "Inactivity Timer"}
                  </span>
                </div>
                <div className="text-2xl font-mono font-bold text-on-surface tracking-tight my-1">
                  {hibernationText}
                </div>
                <p className="text-[11px] text-on-surface-variant/70 leading-relaxed">
                  {status === "HIBERNATED" 
                    ? "Container is paused on disk. Requests to the preview URL or code workspace automatically wake it."
                    : `Hibernates if no HTTP traffic arrives for ${Math.floor(parseInt(idleTimeout) / 60)} minutes.`}
                </p>
              </div>
              <div className="pt-2 border-t border-outline-variant/30 text-[10px] text-on-surface-variant/60">
                Configured: {Math.floor(parseInt(idleTimeout) / 60)}m idle threshold
              </div>
            </div>

            {/* Card 2: Hibernated Expiration */}
            <div className="p-4 rounded-xl bg-surface-container/50 border border-outline-variant/50 flex flex-col justify-between space-y-3">
              <div>
                <div className="flex items-center justify-between text-xs text-on-surface-variant mb-1">
                  <span className="flex items-center gap-1.5 font-semibold text-red-400">
                    <Hourglass className="w-4 h-4" />
                    Hibernated Expiration
                  </span>
                  <span className={`text-[10px] uppercase font-bold tracking-wider px-2 py-0.5 rounded border ${
                    status === "HIBERNATED"
                      ? "bg-red-400/10 text-red-400 border-red-400/20"
                      : "bg-surface-container text-on-surface-variant/70 border-outline-variant"
                  }`}>
                    {status === "HIBERNATED" ? "Sliding Expiry Active" : "Inactive"}
                  </span>
                </div>
                <div className="text-2xl font-mono font-bold text-on-surface tracking-tight my-1">
                  {expirationCountdown}
                </div>
                <p className="text-[11px] text-on-surface-variant/70 leading-relaxed">
                  {status === "HIBERNATED"
                    ? "Sliding expiration active. Waking the sandbox and returning to RUNNING invalidates this countdown."
                    : "Not applicable — expiration countdown only starts after the sandbox hibernates. Waking resets the timer."}
                </p>
              </div>
              <div className="pt-2 border-t border-outline-variant/30 text-[10px] text-on-surface-variant/60 truncate" title={absoluteExpiration}>
                Scheduled: {absoluteExpiration}
              </div>
            </div>

            {/* Card 3: Workspace Data Retention */}
            <div className="p-4 rounded-xl bg-surface-container/50 border border-outline-variant/50 flex flex-col justify-between space-y-3">
              <div>
                <div className="flex items-center justify-between text-xs text-on-surface-variant mb-1">
                  <span className="flex items-center gap-1.5 font-semibold text-blue-400">
                    <HardDrive className="w-4 h-4" />
                    Data Retention
                  </span>
                  <span className="text-[10px] uppercase font-bold tracking-wider px-2 py-0.5 rounded bg-blue-400/10 text-blue-400 border border-blue-400/20">
                    Disk Safety
                  </span>
                </div>
                <div className="text-2xl font-mono font-bold text-on-surface tracking-tight my-1">
                  {retentionText}
                </div>
                <p className="text-[11px] text-on-surface-variant/70 leading-relaxed">
                  Workspace files and commits remain intact even after container expiration until the retention window expires.
                </p>
              </div>
              <div className="pt-2 border-t border-outline-variant/30 text-[10px] text-on-surface-variant/60 truncate" title={retentionDeadline}>
                Cleanup: {retentionDeadline}
              </div>
            </div>
          </div>

          {/* Policy Configuration Controls: 3 Independent Dropdowns */}
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 pt-2">
            <div>
              <label className="block text-xs font-semibold text-on-surface-variant mb-1">
                Idle Inactivity Timeout
              </label>
              <select
                value={idleTimeout}
                onChange={(e) => setIdleTimeout(e.target.value)}
                disabled={isViewerRole}
                className="w-full px-3 py-2 text-xs bg-surface-container/60 border border-outline-variant rounded-lg focus:border-primary-fixed focus:outline-none disabled:opacity-50 text-on-surface cursor-pointer"
              >
                <option value="300">5 minutes</option>
                <option value="600">10 minutes</option>
                <option value="900">15 minutes (Default)</option>
                <option value="1800">30 minutes</option>
                <option value="3600">1 hour</option>
                <option value="7200">2 hours</option>
              </select>
              <p className="text-[10px] text-on-surface-variant/60 mt-1">Inactivity before auto-hibernation</p>
            </div>

            <div>
              <label className="block text-xs font-semibold text-on-surface-variant mb-1">
                Hibernated Expiration Timeout
              </label>
              <select
                value={hibernatedExpiration}
                onChange={(e) => setHibernatedExpiration(e.target.value)}
                disabled={isViewerRole}
                className="w-full px-3 py-2 text-xs bg-surface-container/60 border border-outline-variant rounded-lg focus:border-primary-fixed focus:outline-none disabled:opacity-50 text-on-surface cursor-pointer"
              >
                <option value="1800">30 minutes</option>
                <option value="3600">1 hour</option>
                <option value="7200">2 hours (Default)</option>
                <option value="14400">4 hours</option>
                <option value="28800">8 hours</option>
                <option value="86400">24 hours (1 day)</option>
              </select>
              <p className="text-[10px] text-on-surface-variant/60 mt-1">Sliding countdown while hibernated</p>
            </div>

            <div>
              <label className="block text-xs font-semibold text-on-surface-variant mb-1">
                Workspace Data Retention
              </label>
              <select
                value={workspaceRetention}
                onChange={(e) => setWorkspaceRetention(e.target.value)}
                disabled={isViewerRole}
                className="w-full px-3 py-2 text-xs bg-surface-container/60 border border-outline-variant rounded-lg focus:border-primary-fixed focus:outline-none disabled:opacity-50 text-on-surface cursor-pointer"
              >
                <option value="86400">1 day</option>
                <option value="259200">3 days</option>
                <option value="604800">7 days (Default)</option>
                <option value="1209600">14 days</option>
                <option value="2592000">30 days</option>
              </select>
              <p className="text-[10px] text-on-surface-variant/60 mt-1">Disk preservation post-expiration</p>
            </div>
          </div>

          <div className="flex items-center gap-2 p-3 rounded-lg bg-surface-container/40 border border-outline-variant/30 text-[11px] text-on-surface-variant">
            <ShieldAlert className="w-4 h-4 text-primary-fixed shrink-0" />
            <span>
              {isViewerRole 
                ? "Viewer permissions: Lifecycle configurations are read-only. Contact the project Owner to modify."
                : "Security policy: Health checks and automated probes do not reset the inactivity timer. Only genuine developer traffic keeps sandboxes alive."}
            </span>
          </div>
        </div>

        {/* Runtime & Build Configuration Card */}
        <div className="border border-outline-variant/60 rounded-xl bg-surface-container/30 p-5 space-y-5">
          <div className="pb-3 border-b border-outline-variant/40 flex items-center gap-2">
            <HardDrive className="w-4 h-4 text-primary-fixed" />
            <div>
              <h4 className="text-sm font-bold text-on-surface">Runtime & Build Configuration</h4>
              <p className="text-xs text-on-surface-variant/70">Configure runtime engines, ports, start commands, and health check heuristics.</p>
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-5">
            {/* Left Column */}
            <div className="space-y-4">
              <div>
                <label className="block text-xs font-semibold text-on-surface-variant mb-1">Runtime Type</label>
                <select
                  value={runtimeType}
                  onChange={e => setRuntimeType(e.target.value)}
                  disabled={isViewerRole}
                  className="w-full px-3 py-2 text-xs bg-surface-container/60 border border-outline-variant rounded-lg focus:border-primary-fixed focus:ring-1 focus:ring-primary-fixed outline-none transition-all disabled:opacity-50 text-on-surface"
                >
                  <option value="">Auto-detect</option>
                  <option value="node">Node.js</option>
                  <option value="python">Python</option>
                  <option value="go">Go</option>
                  <option value="docker">Docker Build</option>
                </select>
                <p className="text-[10px] text-on-surface-variant/60 mt-1">If Auto-detect fails, specify the correct language runtime.</p>
              </div>

              <div>
                <label className="block text-xs font-semibold text-on-surface-variant mb-1">Root Directory <span className="text-on-surface-variant/50 font-normal">(Optional)</span></label>
                <input 
                  type="text" 
                  value={rootDirectory}
                  onChange={e => setRootDirectory(e.target.value)}
                  disabled={isViewerRole}
                  placeholder="e.g. backend"
                  className="w-full px-3 py-2 text-xs bg-surface-container/60 border border-outline-variant rounded-lg focus:border-primary-fixed focus:ring-1 focus:ring-primary-fixed outline-none transition-all disabled:opacity-50 text-on-surface"
                />
                <p className="text-[10px] text-on-surface-variant/60 mt-1">If set, commands run from this directory instead of repository root.</p>
              </div>

              <div>
                <label className="block text-xs font-semibold text-on-surface-variant mb-1">Pre-Deploy Command</label>
                <input 
                  type="text" 
                  value={preDeployCommand}
                  onChange={e => setPreDeployCommand(e.target.value)}
                  disabled={isViewerRole}
                  placeholder="e.g. npm run build"
                  className="w-full px-3 py-2 text-xs bg-surface-container/60 border border-outline-variant rounded-lg focus:border-primary-fixed focus:ring-1 focus:ring-primary-fixed outline-none transition-all disabled:opacity-50 text-on-surface"
                />
                <p className="text-[10px] text-on-surface-variant/60 mt-1">Runs before the start command. Useful for database migrations or build steps.</p>
              </div>
            </div>

            {/* Right Column */}
            <div className="space-y-4">
              <div>
                <label className="block text-xs font-semibold text-on-surface-variant mb-1">Container Port Override</label>
                <input 
                  type="number" 
                  value={port}
                  onChange={e => setPort(e.target.value)}
                  disabled={isViewerRole}
                  placeholder="e.g. 8080"
                  className="w-full px-3 py-2 text-xs bg-surface-container/60 border border-outline-variant rounded-lg focus:border-primary-fixed focus:ring-1 focus:ring-primary-fixed outline-none transition-all disabled:opacity-50 text-on-surface"
                />
                <p className="text-[10px] text-on-surface-variant/60 mt-1">The port your application listens on inside the container.</p>
              </div>

              <div>
                <label className="block text-xs font-semibold text-on-surface-variant mb-1">Boot Health Check</label>
                <select
                  value={healthCheckType}
                  onChange={e => setHealthCheckType(e.target.value)}
                  disabled={isViewerRole}
                  className="w-full px-3 py-2 text-xs bg-surface-container/60 border border-outline-variant rounded-lg focus:border-primary-fixed focus:ring-1 focus:ring-primary-fixed outline-none transition-all disabled:opacity-50 text-on-surface"
                >
                  <option value="http">HTTP application readiness (requires 2xx)</option>
                  <option value="tcp">TCP port connection</option>
                  <option value="none">None (For workers or non-listening jobs)</option>
                </select>
                <p className="text-[10px] text-on-surface-variant/60 mt-1">HTTP requires 2xx response. TCP checks connection. None checks process only.</p>
              </div>

              <div>
                <label className="block text-xs font-semibold text-on-surface-variant mb-1">Start Command Override</label>
                <input 
                  type="text" 
                  value={startCommand}
                  onChange={e => setStartCommand(e.target.value)}
                  disabled={isViewerRole}
                  placeholder="e.g. npm run dev"
                  className="w-full px-3 py-2 text-xs bg-surface-container/60 border border-outline-variant rounded-lg focus:border-primary-fixed focus:ring-1 focus:ring-primary-fixed outline-none transition-all disabled:opacity-50 text-on-surface"
                />
                <p className="text-[10px] text-on-surface-variant/60 mt-1">Leave blank to rely on automatic heuristics. Required if Runtime Type is explicit.</p>
              </div>
            </div>

            {runtimeType === 'docker' && (
              <div className="col-span-full">
                <label className="block text-xs font-semibold text-on-surface-variant mb-1">Dockerfile Path</label>
                <input 
                  type="text" 
                  value={dockerfilePath}
                  onChange={e => setDockerfilePath(e.target.value)}
                  disabled={isViewerRole}
                  placeholder="e.g. ./Dockerfile"
                  className="w-full px-3 py-2 text-xs bg-surface-container/60 border border-outline-variant rounded-lg focus:border-primary-fixed focus:ring-1 focus:ring-primary-fixed outline-none transition-all disabled:opacity-50 text-on-surface"
                />
                <p className="text-[10px] text-on-surface-variant/60 mt-1">Path to your Dockerfile, relative to the repo root. Defaults to ./Dockerfile.</p>
              </div>
            )}
          </div>
        </div>

        {/* Environment Variables Manager */}
        <div>
          <EnvironmentVariablesManager envId={(env.id as string) || ""} isViewerRole={isViewerRole} />
        </div>
      </div>

      {/* Footer Save Bar */}
      {!isViewerRole && (
        <div className="bg-surface-container/60 border-t border-outline-variant px-5 py-3.5 flex items-center justify-between shrink-0">
          <p className="text-xs text-on-surface-variant/80">Changes apply on the next restart or wake.</p>
          <button
            onClick={handleSave}
            disabled={isSaving}
            className="px-4 py-1.5 bg-primary-fixed text-on-primary-fixed rounded-lg text-xs font-semibold hover:opacity-90 transition-opacity flex items-center gap-1.5 disabled:opacity-50 cursor-pointer shadow-sm"
          >
            {isSaving ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Save className="w-3.5 h-3.5" />}
            Save Settings
          </button>
        </div>
      )}
    </div>
  )
}
