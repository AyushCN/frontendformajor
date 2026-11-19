"use client";

import { useState } from "react";
import toast from "react-hot-toast";
import {
  ShieldAlert,
  Play,
  Upload,
  SkipForward,
  Loader2,
  CheckCircle2,
  FileText,
} from "lucide-react";

interface RequiredVar {
  key: string;
  example_value?: string;
  configured: boolean;
}

export default function EnvironmentConfigPopup({
  envId,
  requiredVars,
  isOpen,
  onClose,
  onProceed,
}: {
  envId: string;
  requiredVars: RequiredVar[];
  isOpen: boolean;
  onClose: () => void;
  onProceed: () => void;
}) {
  const [values, setValues] = useState<Record<string, string>>({});
  const [isSaving, setIsSaving] = useState(false);
  const [pasteMode, setPasteMode] = useState(false);
  const [rawText, setRawText] = useState("");

  if (!isOpen) return null;

  const unconfigured = requiredVars.filter((r) => !r.configured);

  const handleSaveAndRun = async () => {
    setIsSaving(true);
    try {
      if (pasteMode) {
        if (!rawText.trim()) {
          toast.error("Please enter .env content or switch back");
          setIsSaving(false);
          return;
        }
        const res = await fetch(`/api/environments/${envId}/env/upload`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ content: rawText, strategy: "merge" }),
        });
        if (!res.ok) {
          const errData = await res.json();
          throw new Error(errData.error || "Failed to save configuration");
        }
      } else {
        // Save filled fields
        for (const [key, val] of Object.entries(values)) {
          if (val.trim()) {
            const res = await fetch(`/api/environments/${envId}/env`, {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ key, value: val }),
            });
            if (!res.ok) {
              const errData = await res.json();
              throw new Error(errData.error || `Failed to save ${key}`);
            }
          }
        }
      }

      toast.success("Environment configured successfully");
      onProceed();
    } catch (err: any) {
      toast.error(err.message || "Failed to configure environment");
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4 backdrop-blur-sm animate-in fade-in duration-200">
      <div className="w-full max-w-lg bg-surface border border-outline-variant rounded-2xl p-6 shadow-2xl space-y-5">
        <div className="flex items-start gap-3">
          <div className="p-2 rounded-xl bg-amber-500/10 text-amber-500 border border-amber-500/20 shrink-0">
            <ShieldAlert className="w-6 h-6" />
          </div>
          <div className="flex-1">
            <h3 className="text-lg font-bold text-on-surface">
              Environment Variables Required
            </h3>
            <p className="text-xs text-on-surface-variant mt-0.5">
              This repository specifies environment configuration to run properly. Please provide the required values below.
            </p>
          </div>
        </div>

        {/* Mode Toggle */}
        <div className="flex rounded-lg bg-surface-container p-1 border border-outline-variant">
          <button
            type="button"
            onClick={() => setPasteMode(false)}
            className={`flex-1 py-1.5 text-xs font-semibold rounded-md transition-all ${
              !pasteMode
                ? "bg-surface shadow text-on-surface"
                : "text-on-surface-variant hover:text-on-surface"
            }`}
          >
            Form Fields ({unconfigured.length})
          </button>
          <button
            type="button"
            onClick={() => setPasteMode(true)}
            className={`flex-1 py-1.5 text-xs font-semibold rounded-md transition-all ${
              pasteMode
                ? "bg-surface shadow text-on-surface"
                : "text-on-surface-variant hover:text-on-surface"
            }`}
          >
            Upload / Paste .env
          </button>
        </div>

        {pasteMode ? (
          <div>
            <textarea
              value={rawText}
              onChange={(e) => setRawText(e.target.value)}
              placeholder="PORT=8080&#10;DATABASE_URL=...&#10;API_KEY=..."
              rows={6}
              className="w-full p-3 font-mono text-xs rounded-xl border border-outline-variant bg-surface-container/30 focus:outline-none focus:border-primary"
            />
          </div>
        ) : (
          <div className="space-y-3 max-h-60 overflow-y-auto pr-1">
            {unconfigured.map((r) => (
              <div key={r.key} className="space-y-1">
                <div className="flex justify-between items-center text-xs">
                  <span className="font-mono font-semibold text-on-surface">
                    {r.key}
                  </span>
                  {r.example_value && (
                    <span className="text-on-surface-variant/60 font-mono text-[11px] truncate max-w-[200px]">
                      eg: {r.example_value}
                    </span>
                  )}
                </div>
                <input
                  type="text"
                  placeholder={r.example_value || "Enter value..."}
                  value={values[r.key] || ""}
                  onChange={(e) =>
                    setValues({ ...values, [r.key]: e.target.value })
                  }
                  className="w-full px-3 py-1.5 text-xs font-mono rounded-lg border border-outline-variant bg-surface focus:outline-none focus:border-primary"
                />
              </div>
            ))}
          </div>
        )}

        <div className="flex items-center justify-between pt-2 border-t border-outline-variant">
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1.5 text-xs text-on-surface-variant hover:text-on-surface flex items-center gap-1.5"
          >
            <SkipForward className="w-3.5 h-3.5" />
            <span>Skip for now</span>
          </button>

          <div className="flex gap-2">
            <button
              type="button"
              onClick={handleSaveAndRun}
              disabled={isSaving}
              className="px-4 py-2 text-xs font-semibold rounded-lg bg-primary text-on-primary hover:opacity-90 transition-opacity flex items-center gap-2 disabled:opacity-50"
            >
              {isSaving ? (
                <Loader2 className="w-4 h-4 animate-spin" />
              ) : (
                <Play className="w-4 h-4" />
              )}
              <span>Save & Launch Sandbox</span>
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
