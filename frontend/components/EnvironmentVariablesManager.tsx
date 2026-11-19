"use client";

import { useState, useRef } from "react";
import useSWR from "swr";
import toast from "react-hot-toast";
import {
  Lock,
  Unlock,
  Plus,
  Trash2,
  Upload,
  RefreshCw,
  Eye,
  EyeOff,
  AlertCircle,
  CheckCircle2,
  FileCode,
  Loader2,
  ShieldCheck,
} from "lucide-react";
import { fetchWithAuth } from "@/lib/auth";

interface EnvVar {
  key: string;
  value: string;
  masked: boolean;
  is_ai_suggested?: boolean;
  created_at?: string;
  updated_at?: string;
}

interface EnvResponse {
  sandbox_id: string;
  env_vars: EnvVar[];
  can_manage: boolean;
}

interface RequirementsResponse {
  required: { key: string; example_value?: string; configured: boolean }[];
  missing: number;
  has_saved: boolean;
}

export default function EnvironmentVariablesManager({
  envId,
  isViewerRole,
}: {
  envId: string;
  isViewerRole: boolean;
}) {
  const [reveal, setReveal] = useState(false);
  const [newKey, setNewKey] = useState("");
  const [newValue, setNewValue] = useState("");
  const [isAdding, setIsAdding] = useState(false);
  const [isApplying, setIsApplying] = useState(false);
  const [isUploading, setIsUploading] = useState(false);
  const [showUploadModal, setShowUploadModal] = useState(false);
  const [uploadContent, setUploadContent] = useState("");
  const [uploadStrategy, setUploadStrategy] = useState<"merge" | "replace">("merge");
  const fileInputRef = useRef<HTMLInputElement>(null);

  // Fetch variables
  const { data, mutate, isValidating } = useSWR<EnvResponse>(
    envId ? `/api/environments/${envId}/env?reveal=${reveal}` : null,
    fetchWithAuth,
    { revalidateOnFocus: false }
  );

  // Fetch requirements (.env.example detection)
  const { data: requirements, mutate: mutateRequirements } = useSWR<RequirementsResponse>(
    envId ? `/api/environments/${envId}/env/requirements` : null,
    fetchWithAuth
  );

  const canManage = data?.can_manage ?? !isViewerRole;
  const envVars = data?.env_vars || [];

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newKey.trim()) {
      toast.error("Variable name is required");
      return;
    }

    setIsAdding(true);
    try {
      const res = await fetch(`/api/environments/${envId}/env`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ key: newKey.trim(), value: newValue }),
      });

      const resData = await res.json();
      if (!res.ok) {
        throw new Error(resData.error || "Failed to save variable");
      }

      toast.success(`Saved ${newKey.trim()}`);
      setNewKey("");
      setNewValue("");
      mutate();
      mutateRequirements();
    } catch (err: any) {
      toast.error(err.message || "Failed to save variable");
    } finally {
      setIsAdding(false);
    }
  };

  const handleDelete = async (key: string) => {
    if (!confirm(`Delete environment variable ${key}?`)) return;

    try {
      const res = await fetch(`/api/environments/${envId}/env/${encodeURIComponent(key)}`, {
        method: "DELETE",
      });
      if (!res.ok) {
        const resData = await res.json();
        throw new Error(resData.error || "Failed to delete variable");
      }
      toast.success(`Deleted ${key}`);
      mutate();
      mutateRequirements();
    } catch (err: any) {
      toast.error(err.message || "Failed to delete variable");
    }
  };

  const handleApply = async () => {
    setIsApplying(true);
    try {
      const res = await fetch(`/api/environments/${envId}/env/apply`, {
        method: "POST",
      });
      const resData = await res.json();
      if (!res.ok) {
        throw new Error(resData.error || "Failed to apply environment changes");
      }
      toast.success("Restarting container with latest environment variables...");
    } catch (err: any) {
      toast.error(err.message || "Failed to apply environment variables");
    } finally {
      setIsApplying(false);
    }
  };

  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;

    const reader = new FileReader();
    reader.onload = (event) => {
      setUploadContent(event.target?.result as string || "");
    };
    reader.readAsText(file);
  };

  const handleUploadSubmit = async () => {
    if (!uploadContent.trim()) {
      toast.error("Please select a file or paste .env content");
      return;
    }

    setIsUploading(true);
    try {
      const res = await fetch(`/api/environments/${envId}/env/upload`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          content: uploadContent,
          strategy: uploadStrategy,
        }),
      });

      const resData = await res.json();
      if (!res.ok) {
        throw new Error(resData.error || "Failed to upload .env");
      }

      toast.success(
        `Applied ${resData.applied} variable(s)${
          resData.skipped > 0 ? ` (${resData.skipped} skipped)` : ""
        }`
      );
      setShowUploadModal(false);
      setUploadContent("");
      mutate();
      mutateRequirements();
    } catch (err: any) {
      toast.error(err.message || "Upload failed");
    } finally {
      setIsUploading(false);
    }
  };

  return (
    <div className="space-y-6">
      {/* Header Banner */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 p-4 rounded-xl bg-surface-container/60 border border-outline-variant">
        <div>
          <div className="flex items-center gap-2">
            <ShieldCheck className="w-5 h-5 text-primary" />
            <h3 className="text-base font-semibold text-on-surface">
              Secure Environment Variables
            </h3>
          </div>
          <p className="text-xs text-on-surface-variant mt-1">
            Variables are encrypted with AES-256-GCM at rest, masked in the editor, and injected at container runtime.
          </p>
        </div>

        <div className="flex items-center gap-2">
          {canManage && (
            <button
              onClick={() => setReveal(!reveal)}
              disabled={isValidating}
              className="px-3 py-1.5 text-xs font-medium rounded-lg border border-outline-variant bg-surface hover:bg-surface-container transition-colors flex items-center gap-1.5"
              title={reveal ? "Hide Secret Values" : "Reveal Plaintext Secrets"}
            >
              {reveal ? (
                <>
                  <EyeOff className="w-3.5 h-3.5 text-amber-500" />
                  <span>Mask Values</span>
                </>
              ) : (
                <>
                  <Eye className="w-3.5 h-3.5" />
                  <span>Reveal Values</span>
                </>
              )}
            </button>
          )}

          {canManage && (
            <button
              onClick={() => setShowUploadModal(true)}
              className="px-3 py-1.5 text-xs font-medium rounded-lg border border-outline-variant bg-surface hover:bg-surface-container transition-colors flex items-center gap-1.5"
            >
              <Upload className="w-3.5 h-3.5" />
              <span>Upload .env</span>
            </button>
          )}

          {canManage && (
            <button
              onClick={handleApply}
              disabled={isApplying}
              className="px-3 py-1.5 text-xs font-semibold rounded-lg bg-primary text-on-primary hover:opacity-90 transition-opacity flex items-center gap-1.5 disabled:opacity-50"
            >
              <RefreshCw className={`w-3.5 h-3.5 ${isApplying ? "animate-spin" : ""}`} />
              <span>Apply to Runtime</span>
            </button>
          )}
        </div>
      </div>

      {/* Missing Requirements Alert */}
      {requirements && requirements.missing > 0 && (
        <div className="p-4 rounded-xl border border-amber-500/30 bg-amber-500/10 flex items-start gap-3">
          <AlertCircle className="w-5 h-5 text-amber-500 shrink-0 mt-0.5" />
          <div className="flex-1">
            <h4 className="text-sm font-semibold text-amber-500">
              {requirements.missing} required variable{requirements.missing > 1 ? "s" : ""} not yet configured
            </h4>
            <p className="text-xs text-on-surface-variant mt-0.5">
              The project template (.env.example) declares the following variables needed for execution:
            </p>
            <div className="flex flex-wrap gap-1.5 mt-2">
              {requirements.required
                .filter((r) => !r.configured)
                .map((r) => (
                  <button
                    key={r.key}
                    onClick={() => {
                      setNewKey(r.key);
                      setNewValue(r.example_value || "");
                    }}
                    className="px-2 py-1 rounded bg-amber-500/20 text-amber-200 text-xs font-mono hover:bg-amber-500/30 transition-colors"
                    title={`Click to fill: ${r.key}`}
                  >
                    + {r.key}
                  </button>
                ))}
            </div>
          </div>
        </div>
      )}

      {/* Variables Table */}
      <div className="border border-outline-variant rounded-xl overflow-hidden bg-surface">
        <div className="px-4 py-3 border-b border-outline-variant bg-surface-container/30 flex items-center justify-between">
          <span className="text-xs font-semibold text-on-surface-variant uppercase tracking-wider">
            Configured Variables ({envVars.length})
          </span>
          <span className="text-xs text-on-surface-variant">
            {reveal ? "Showing unmasked plaintext" : "Masked with AES-256"}
          </span>
        </div>

        {envVars.length === 0 ? (
          <div className="p-8 text-center text-sm text-on-surface-variant italic">
            No environment variables configured. Add one below or upload an existing .env file.
          </div>
        ) : (
          <div className="divide-y divide-outline-variant">
            {envVars.map((v) => (
              <div
                key={v.key}
                className="px-4 py-3 flex items-center justify-between gap-4 hover:bg-surface-container/20 transition-colors"
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div className="p-1.5 rounded-lg bg-surface-container border border-outline-variant shrink-0">
                    {v.masked ? (
                      <Lock className="w-3.5 h-3.5 text-on-surface-variant" />
                    ) : (
                      <Unlock className="w-3.5 h-3.5 text-primary" />
                    )}
                  </div>
                  <div className="min-w-0">
                    <p className="text-sm font-mono font-semibold text-on-surface truncate">
                      {v.key}
                    </p>
                    <p className="text-xs font-mono text-on-surface-variant truncate">
                      {v.value}
                    </p>
                  </div>
                </div>

                {canManage && (
                  <button
                    onClick={() => handleDelete(v.key)}
                    className="p-1.5 text-on-surface-variant hover:text-error hover:bg-error/10 rounded-lg transition-colors shrink-0"
                    title={`Delete ${v.key}`}
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                )}
              </div>
            ))}
          </div>
        )}

        {/* Add Form */}
        {canManage && (
          <form
            onSubmit={handleAdd}
            className="p-4 border-t border-outline-variant bg-surface-container/20 flex flex-col sm:flex-row gap-3"
          >
            <input
              type="text"
              placeholder="VARIABLE_NAME"
              value={newKey}
              onChange={(e) => setNewKey(e.target.value.toUpperCase())}
              className="flex-1 px-3 py-2 text-sm font-mono rounded-lg border border-outline-variant bg-surface focus:outline-none focus:border-primary"
            />
            <input
              type="text"
              placeholder="value"
              value={newValue}
              onChange={(e) => setNewValue(e.target.value)}
              className="flex-1 px-3 py-2 text-sm font-mono rounded-lg border border-outline-variant bg-surface focus:outline-none focus:border-primary"
            />
            <button
              type="submit"
              disabled={isAdding || !newKey.trim()}
              className="px-4 py-2 bg-primary text-on-primary rounded-lg text-sm font-semibold hover:opacity-90 transition-opacity flex items-center justify-center gap-2 disabled:opacity-50 shrink-0"
            >
              {isAdding ? <Loader2 className="w-4 h-4 animate-spin" /> : <Plus className="w-4 h-4" />}
              <span>Add</span>
            </button>
          </form>
        )}
      </div>

      {/* Upload Modal */}
      {showUploadModal && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
          <div className="w-full max-w-lg bg-surface border border-outline-variant rounded-2xl p-6 shadow-2xl space-y-4">
            <div className="flex items-center justify-between">
              <h3 className="text-lg font-semibold text-on-surface flex items-center gap-2">
                <FileCode className="w-5 h-5 text-primary" />
                Upload or Paste .env
              </h3>
              <button
                onClick={() => setShowUploadModal(false)}
                className="text-on-surface-variant hover:text-on-surface"
              >
                ✕
              </button>
            </div>

            <p className="text-xs text-on-surface-variant">
              Select an existing .env file from your local machine, or paste raw key-value pairs below. Standard comments (#) and quotes are supported.
            </p>

            <div className="space-y-3">
              <div>
                <input
                  type="file"
                  ref={fileInputRef}
                  onChange={handleFileChange}
                  accept=".env,text/plain"
                  className="hidden"
                />
                <button
                  type="button"
                  onClick={() => fileInputRef.current?.click()}
                  className="w-full py-2.5 px-4 rounded-xl border border-dashed border-outline-variant hover:border-primary hover:bg-surface-container/50 text-xs text-on-surface-variant flex items-center justify-center gap-2 transition-colors"
                >
                  <Upload className="w-4 h-4" />
                  <span>Choose local .env file</span>
                </button>
              </div>

              <div>
                <label className="block text-xs font-medium text-on-surface-variant mb-1">
                  Or paste content:
                </label>
                <textarea
                  value={uploadContent}
                  onChange={(e) => setUploadContent(e.target.value)}
                  placeholder="PORT=8080&#10;DATABASE_URL=postgres://...&#10;API_KEY=secret_123"
                  rows={6}
                  className="w-full p-3 font-mono text-xs rounded-xl border border-outline-variant bg-surface-container/30 focus:outline-none focus:border-primary"
                />
              </div>

              <div>
                <label className="block text-xs font-medium text-on-surface-variant mb-1">
                  Conflict Resolution Strategy:
                </label>
                <div className="flex gap-4">
                  <label className="flex items-center gap-2 text-xs text-on-surface cursor-pointer">
                    <input
                      type="radio"
                      name="strategy"
                      value="merge"
                      checked={uploadStrategy === "merge"}
                      onChange={() => setUploadStrategy("merge")}
                    />
                    <span>Merge (keep existing values on key conflict)</span>
                  </label>
                  <label className="flex items-center gap-2 text-xs text-on-surface cursor-pointer">
                    <input
                      type="radio"
                      name="strategy"
                      value="replace"
                      checked={uploadStrategy === "replace"}
                      onChange={() => setUploadStrategy("replace")}
                    />
                    <span>Replace all</span>
                  </label>
                </div>
              </div>
            </div>

            <div className="flex justify-end gap-2 pt-2">
              <button
                type="button"
                onClick={() => setShowUploadModal(false)}
                className="px-4 py-2 text-xs font-semibold rounded-lg border border-outline-variant hover:bg-surface-container transition-colors"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={handleUploadSubmit}
                disabled={isUploading || !uploadContent.trim()}
                className="px-4 py-2 text-xs font-semibold rounded-lg bg-primary text-on-primary hover:opacity-90 transition-opacity flex items-center gap-2 disabled:opacity-50"
              >
                {isUploading ? <Loader2 className="w-4 h-4 animate-spin" /> : null}
                <span>Parse & Save Encrypted</span>
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
