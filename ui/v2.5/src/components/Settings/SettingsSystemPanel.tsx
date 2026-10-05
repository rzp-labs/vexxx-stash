import React, { useEffect, useRef, useState } from "react";
import * as GQL from "src/core/generated-graphql";
import { LoadingIndicator } from "src/components/Shared/LoadingIndicator";
import { SettingSection } from "./SettingSection";
import {
  BooleanSetting,
  ModalSetting,
  NumberSetting,
  SelectSetting,
  Setting,
  StringListSetting,
  StringSetting,
} from "./Inputs";
import { useSettings } from "./context";
import {
  VideoPreviewInput,
  VideoPreviewSettingsInput,
} from "./GeneratePreviewOptions";
import { FormattedMessage, useIntl } from "react-intl";
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Typography,
} from "@mui/material";
import { useToast } from "src/hooks/Toast";
import { useHistory } from "react-router-dom";
import {
  normalizeGenerationSettings,
  validateGenerationSettings,
} from "./generationSettingsValidation";
import { NumberField } from "src/utils/form";

// Save generation requests together, so coupled GPU/total limits are validated
// as a complete proposal. Running values come only from the server snapshot.
export const IntelGenerationSettings: React.FC = () => {
  const intl = useIntl();
  const { data, client } = GQL.useConfigurationQuery();
  const persisted = data?.configuration.general;
  const [draft, setDraft] = useState<GQL.ConfigGeneralInput>({});
  const [saveError, setSaveError] = useState<string>();
  const dirtyFields = useRef(new Set<keyof GQL.ConfigGeneralInput>());
  const previousSaved = useRef<GQL.ConfigGeneralInput>();
  const [externalChanges, setExternalChanges] = useState(false);
  const [save, { loading: saving }] = GQL.useConfigureGeneralMutation({
    refetchQueries: [GQL.ConfigurationDocument],
    awaitRefetchQueries: true,
  });

  const generationLoaded = !!persisted;
  const {
    generationMarkerBackend,
    generationSpriteBackend,
    generationPreviewBackend,
    generationDevice,
    generationBudgetEnabled,
    generationMaxProcesses,
    generationMaxGPUProcesses,
    generationThreads,
  } = persisted ?? {};
  useEffect(() => {
    if (!generationLoaded) return;
    const incoming = normalizeGenerationSettings({
      generationMarkerBackend,
      generationSpriteBackend,
      generationPreviewBackend,
      generationDevice,
      generationBudgetEnabled,
      generationMaxProcesses,
      generationMaxGPUProcesses,
      generationThreads,
    });
    if (
      dirtyFields.current.size &&
      previousSaved.current &&
      Object.entries(incoming).some(
        ([key, value]) =>
          previousSaved.current![key as keyof GQL.ConfigGeneralInput] !== value
      )
    ) {
      setExternalChanges(true);
    }
    previousSaved.current = incoming;
    setDraft((current) =>
      Object.fromEntries(
        Object.entries(incoming).map(([key, value]) => [
          key,
          dirtyFields.current.has(key as keyof GQL.ConfigGeneralInput)
            ? current[key as keyof GQL.ConfigGeneralInput]
            : value,
        ])
      )
    );
  }, [
    generationLoaded,
    generationMarkerBackend,
    generationSpriteBackend,
    generationPreviewBackend,
    generationDevice,
    generationBudgetEnabled,
    generationMaxProcesses,
    generationMaxGPUProcesses,
    generationThreads,
  ]);

  if (!persisted) return null;
  const active = persisted.activeGeneration;
  const validation = validateGenerationSettings(draft);
  const changed = Object.entries(draft).some(
    ([key, value]) => persisted[key as keyof typeof persisted] !== value
  );
  const change = (patch: Partial<GQL.ConfigGeneralInput>) => {
    const savedProposal = normalizeGenerationSettings(persisted);
    for (const [key, value] of Object.entries(patch)) {
      const field = key as keyof GQL.ConfigGeneralInput;
      if (value === savedProposal[field]) dirtyFields.current.delete(field);
      else dirtyFields.current.add(field);
    }
    if (!dirtyFields.current.size) setExternalChanges(false);
    setDraft((current) => ({ ...current, ...patch }));
    setSaveError(undefined);
  };
  const submit = async () => {
    if (validation || saving) return;
    try {
      await save({ variables: { input: draft } });
      // The awaited refetch can include a newer external save than the mutation
      // response. Read its cache directly, even if React has not rendered it yet.
      const confirmed = client.readQuery<GQL.ConfigurationQuery>({
        query: GQL.ConfigurationDocument,
      })?.configuration.general;
      if (!confirmed)
        throw new Error(
          intl.formatMessage({
            id: "config.general.generation.confirmation_unavailable",
          })
        );
      const saved = normalizeGenerationSettings(confirmed);
      previousSaved.current = saved;
      dirtyFields.current.clear();
      setDraft(saved);
      setExternalChanges(false);
      setSaveError(undefined);
    } catch (e) {
      setSaveError(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <SettingSection headingID="config.general.generation.heading">
      <Box
        component="fieldset"
        disabled={saving}
        sx={{ border: 0, p: 0, m: 0, minWidth: 0 }}
      >
        <Alert
          severity={
            persisted.generationConfigurationError
              ? "error"
              : persisted.generationRestartRequired
              ? "warning"
              : "info"
          }
        >
          <FormattedMessage
            id={
              persisted.generationConfigurationError
                ? "config.general.generation.invalid_saved"
                : persisted.generationRestartRequired
                ? "config.general.generation.pending_restart"
                : "config.general.generation.restart_description"
            }
          />
        </Alert>
        {externalChanges && (
          <Alert severity="warning">
            <FormattedMessage id="config.general.generation.external_change" />
          </Alert>
        )}
        <Typography variant="body2" sx={{ my: 1 }}>
          <FormattedMessage
            id="config.general.generation.active"
            values={{
              marker: active.markerBackend,
              sprite: active.spriteBackend,
              preview: active.previewBackend,
              device: active.device,
              budget: active.budgetEnabled
                ? intl.formatMessage({
                    id: "config.general.generation.enabled",
                  })
                : intl.formatMessage({
                    id: "config.general.generation.disabled",
                  }),
              processes: active.maxProcesses,
              gpu: active.maxGPUProcesses,
              threads: active.threads,
            }}
          />
        </Typography>
        <Typography variant="body2" sx={{ mb: 1 }}>
          <FormattedMessage
            id="config.general.generation.persisted"
            values={{
              marker: persisted.generationMarkerBackend,
              sprite: persisted.generationSpriteBackend,
              preview: persisted.generationPreviewBackend,
            }}
          />
        </Typography>
        {persisted.generationConfigurationError && (
          <Alert severity="error">
            {persisted.generationConfigurationError}
          </Alert>
        )}
        <Typography variant="body2" sx={{ mb: 1 }}>
          <FormattedMessage id="config.general.generation.diagnostics" />
        </Typography>
        {(
          [
            "generationMarkerBackend",
            "generationSpriteBackend",
            "generationPreviewBackend",
          ] as const
        ).map((key) => (
          <SelectSetting
            key={key}
            id={key}
            headingID={`config.general.generation.${key}`}
            value={draft[key] ?? "software"}
            disabled={saving}
            onChange={(v) => change({ [key]: v })}
          >
            <option value="software">
              {intl.formatMessage({
                id: "config.general.generation.software",
              })}
            </option>
            <option value="vaapi">Intel VAAPI</option>
            {key !== "generationPreviewBackend" && (
              <option value="qsv" disabled={key === "generationMarkerBackend"}>
                {key === "generationMarkerBackend"
                  ? intl.formatMessage({
                      id: "config.general.generation.qsv_marker_pending",
                    })
                  : "Intel QSV"}
              </option>
            )}
          </SelectSetting>
        ))}
        {(draft.generationMarkerBackend === "qsv" ||
          persisted.generationMarkerBackend === "qsv") && (
          <Alert severity="warning">
            <FormattedMessage id="config.general.generation.qsv_marker_fallback" />
          </Alert>
        )}
        <StringSetting
          id="generation-device"
          headingID="config.general.generation.device"
          subHeadingID="config.general.generation.device_description"
          value={draft.generationDevice ?? "/dev/dri/renderD128"}
          disabled={saving}
          onChange={(v) => change({ generationDevice: v })}
        />
        <BooleanSetting
          id="generation-budget"
          headingID="config.general.generation.budget"
          subHeadingID="config.general.generation.budget_description"
          checked={draft.generationBudgetEnabled ?? false}
          disabled={saving}
          onChange={(v) => change({ generationBudgetEnabled: v })}
        />
        {(
          [
            "generationMaxProcesses",
            "generationMaxGPUProcesses",
            "generationThreads",
          ] as const
        ).map((key) => (
          <ModalSetting<number>
            key={key}
            id={key}
            headingID={`config.general.generation.${key}`}
            subHeadingID="config.general.generation.limits_description"
            value={draft[key] ?? 0}
            disabled={saving}
            onChange={(v) => change({ [key]: v })}
            renderValue={(v) => <span>{v}</span>}
            renderField={(value, setValue) => (
              <NumberField
                min={0}
                max={64}
                step={1}
                value={value ?? 0}
                onChange={(e) => setValue(Number(e.target.value))}
              />
            )}
          />
        ))}
        {validation && (
          <Alert severity="error">
            <FormattedMessage id={validation} />
          </Alert>
        )}
        {saveError && <Alert severity="error">{saveError}</Alert>}
        <Box sx={{ display: "flex", gap: 1, mt: 1 }}>
          <Button
            variant="contained"
            disabled={
              saving ||
              (!changed &&
                !dirtyFields.current.size &&
                !persisted.generationConfigurationError) ||
              !!validation
            }
            onClick={submit}
          >
            <FormattedMessage id="config.general.generation.save" />
          </Button>
          <Button
            disabled={saving}
            onClick={() =>
              change({
                generationMarkerBackend: "software",
                generationSpriteBackend: "software",
                generationPreviewBackend: "software",
                generationBudgetEnabled: false,
              })
            }
          >
            <FormattedMessage id="config.general.generation.rollback" />
          </Button>
        </Box>
      </Box>
    </SettingSection>
  );
};

const ResolvedPathHint: React.FC<{ value?: string }> = ({ value }) => {
  if (!value) return null;

  return (
    <Box
      component="span"
      sx={{
        display: "block",
        mt: 0.5,
        fontFamily: "monospace",
        wordBreak: "break-all",
      }}
    >
      ↳ {value}
    </Box>
  );
};

export const SettingsConfigurationPanel: React.FC = () => {
  const intl = useIntl();
  const Toast = useToast();
  const history = useHistory();
  const [downloadingFFMpeg, setDownloadingFFMpeg] = useState(false);

  const { general, loading, error, saveGeneral } = useSettings();
  const [mutateDownloadFFMpeg] = GQL.useDownloadFfMpegMutation();

  const { data: systemStatusData } = GQL.useSystemStatusQuery();
  const vipsPath = systemStatusData?.systemStatus.vipsPath;

  // Empty when no GPU backend was found, in which case turning native
  // generation on would change nothing and the toggle says so instead.
  const nativeBackend = systemStatusData?.systemStatus.nativeGenerationBackend;

  const transcodeQualities = [
    GQL.StreamingResolutionEnum.Low,
    GQL.StreamingResolutionEnum.Standard,
    GQL.StreamingResolutionEnum.StandardHd,
    GQL.StreamingResolutionEnum.FullHd,
    GQL.StreamingResolutionEnum.FourK,
    GQL.StreamingResolutionEnum.Original,
  ].map(resolutionToString);

  function resolutionToString(r: GQL.StreamingResolutionEnum | undefined) {
    switch (r) {
      case GQL.StreamingResolutionEnum.Low:
        return "240p";
      case GQL.StreamingResolutionEnum.Standard:
        return "480p";
      case GQL.StreamingResolutionEnum.StandardHd:
        return "720p";
      case GQL.StreamingResolutionEnum.FullHd:
        return "1080p";
      case GQL.StreamingResolutionEnum.FourK:
        return "4k";
      case GQL.StreamingResolutionEnum.Original:
        return "Original";
    }

    return "Original";
  }

  function translateQuality(quality: string) {
    switch (quality) {
      case "240p":
        return GQL.StreamingResolutionEnum.Low;
      case "480p":
        return GQL.StreamingResolutionEnum.Standard;
      case "720p":
        return GQL.StreamingResolutionEnum.StandardHd;
      case "1080p":
        return GQL.StreamingResolutionEnum.FullHd;
      case "4k":
        return GQL.StreamingResolutionEnum.FourK;
      case "Original":
        return GQL.StreamingResolutionEnum.Original;
    }

    return GQL.StreamingResolutionEnum.Original;
  }

  const namingHashAlgorithms = [
    GQL.HashAlgorithm.Md5,
    GQL.HashAlgorithm.Oshash,
  ].map(namingHashToString);

  function namingHashToString(value: GQL.HashAlgorithm | undefined) {
    switch (value) {
      case GQL.HashAlgorithm.Oshash:
        return "oshash";
      case GQL.HashAlgorithm.Md5:
        return "MD5";
    }

    return "MD5";
  }

  function translateNamingHash(value: string) {
    switch (value) {
      case "oshash":
        return GQL.HashAlgorithm.Oshash;
      case "MD5":
        return GQL.HashAlgorithm.Md5;
    }

    return GQL.HashAlgorithm.Md5;
  }

  function blobStorageTypeToID(value: GQL.BlobsStorageType | undefined) {
    switch (value) {
      case GQL.BlobsStorageType.Database:
        return "blobs_storage_type.database";
      case GQL.BlobsStorageType.Filesystem:
        return "blobs_storage_type.filesystem";
    }

    return "blobs_storage_type.database";
  }

  async function onDownloadFFMpeg() {
    setDownloadingFFMpeg(true);
    try {
      await mutateDownloadFFMpeg();
      // navigate to tasks page to see the progress
      history.push("/settings?tab=tasks");
    } catch (e) {
      Toast.error(e);
    } finally {
      setDownloadingFFMpeg(false);
    }
  }

  if (error) return <Alert severity="error">{error.message}</Alert>;
  if (loading) return <LoadingIndicator />;

  // The general state is ConfigGeneralInput but the data spread includes
  // read-only resolved abs-path fields from ConfigGeneralDataFragment.
  type GeneralWithAbsPaths = GQL.ConfigGeneralInput & {
    generatedPathAbs?: string;
    cachePathAbs?: string;
    scrapersPathAbs?: string;
    pluginsPathAbs?: string;
    metadataPathAbs?: string;
    databasePathAbs?: string;
  };
  const g = general as GeneralWithAbsPaths;

  return (
    <>
      <SettingSection headingID="config.application_paths.heading">
        <StringSetting
          id="generated-path"
          headingID="config.general.generated_path_head"
          subHeading={
            <>
              {intl.formatMessage({
                id: "config.general.generated_files_location",
              })}
              <ResolvedPathHint value={g.generatedPathAbs} />
            </>
          }
          value={general.generatedPath ?? undefined}
          onChange={(v) => saveGeneral({ generatedPath: v })}
        />

        <StringSetting
          id="cache-path"
          headingID="config.general.cache_path_head"
          subHeading={
            <>
              {intl.formatMessage({ id: "config.general.cache_location" })}
              <ResolvedPathHint value={g.cachePathAbs} />
            </>
          }
          value={general.cachePath ?? undefined}
          onChange={(v) => saveGeneral({ cachePath: v })}
        />

        <StringSetting
          id="scrapers-path"
          headingID="config.general.scrapers_path.heading"
          subHeading={
            <>
              {intl.formatMessage({
                id: "config.general.scrapers_path.description",
              })}
              <ResolvedPathHint value={g.scrapersPathAbs} />
            </>
          }
          value={general.scrapersPath ?? undefined}
          onChange={(v) => saveGeneral({ scrapersPath: v })}
        />

        <StringSetting
          id="plugins-path"
          headingID="config.general.plugins_path.heading"
          subHeading={
            <>
              {intl.formatMessage({
                id: "config.general.plugins_path.description",
              })}
              <ResolvedPathHint value={g.pluginsPathAbs} />
            </>
          }
          value={general.pluginsPath ?? undefined}
          onChange={(v) => saveGeneral({ pluginsPath: v })}
        />

        <StringSetting
          id="metadata-path"
          headingID="config.general.metadata_path.heading"
          subHeading={
            <>
              {intl.formatMessage({
                id: "config.general.metadata_path.description",
              })}
              <ResolvedPathHint value={g.metadataPathAbs} />
            </>
          }
          value={general.metadataPath ?? undefined}
          onChange={(v) => saveGeneral({ metadataPath: v })}
        />

        <StringSetting
          id="custom-performer-image-location"
          headingID="config.ui.performers.options.image_location.heading"
          subHeadingID="config.ui.performers.options.image_location.description"
          value={general.customPerformerImageLocation ?? undefined}
          onChange={(v) => saveGeneral({ customPerformerImageLocation: v })}
        />

        <StringSetting
          id="ffmpeg-path"
          headingID="config.general.ffmpeg.ffmpeg_path.heading"
          subHeadingID="config.general.ffmpeg.ffmpeg_path.description"
          value={general.ffmpegPath ?? undefined}
          onChange={(v) => saveGeneral({ ffmpegPath: v })}
        />

        <StringSetting
          id="ffprobe-path"
          headingID="config.general.ffmpeg.ffprobe_path.heading"
          subHeadingID="config.general.ffmpeg.ffprobe_path.description"
          value={general.ffprobePath ?? undefined}
          onChange={(v) => saveGeneral({ ffprobePath: v })}
        />

        <Setting
          heading={
            <>
              <FormattedMessage id="config.general.ffmpeg.download_ffmpeg.heading" />
            </>
          }
          subHeadingID="config.general.ffmpeg.download_ffmpeg.description"
        >
          <Button
            variant="outlined"
            onClick={() => onDownloadFFMpeg()}
            disabled={downloadingFFMpeg}
            startIcon={
              downloadingFFMpeg ? <CircularProgress size={16} /> : undefined
            }
          >
            <FormattedMessage id="config.general.ffmpeg.download_ffmpeg.heading" />
          </Button>
        </Setting>

        <Setting
          heading="Vips Status"
          subHeading={
            <>
              Optional high-performance image processing library. Install
              libvips on your system to enable 4x-8x faster image thumbnailing.
              {vipsPath && (
                <Box
                  component="span"
                  sx={{
                    display: "block",
                    mt: 0.5,
                    fontFamily: "monospace",
                    wordBreak: "break-all",
                  }}
                >
                  ↳ {vipsPath}
                </Box>
              )}
            </>
          }
        >
          <Typography
            variant="body2"
            sx={{
              fontWeight: 600,
              color: vipsPath ? "success.main" : "text.secondary",
            }}
          >
            {vipsPath ? "Found & Active" : "Not Found (Using FFmpeg fallback)"}
          </Typography>
        </Setting>

        <StringSetting
          id="python-path"
          headingID="config.general.python_path.heading"
          subHeadingID="config.general.python_path.description"
          value={general.pythonPath ?? undefined}
          onChange={(v) => saveGeneral({ pythonPath: v })}
        />

        <StringSetting
          id="backup-directory-path"
          headingID="config.general.backup_directory_path.heading"
          subHeadingID="config.general.backup_directory_path.description"
          value={general.backupDirectoryPath ?? undefined}
          onChange={(v) => saveGeneral({ backupDirectoryPath: v })}
        />

        <StringSetting
          id="delete-trash-path"
          headingID="config.general.delete_trash_path.heading"
          subHeadingID="config.general.delete_trash_path.description"
          value={general.deleteTrashPath ?? undefined}
          onChange={(v) => saveGeneral({ deleteTrashPath: v })}
        />
      </SettingSection>

      <SettingSection headingID="config.general.database">
        <StringSetting
          id="database-path"
          headingID="config.general.db_path_head"
          subHeading={
            <>
              {intl.formatMessage({ id: "config.general.sqlite_location" })}
              <ResolvedPathHint value={g.databasePathAbs} />
            </>
          }
          value={general.databasePath ?? undefined}
          onChange={(v) => saveGeneral({ databasePath: v })}
        />
        <SelectSetting
          id="blobs-storage"
          headingID="config.general.blobs_storage.heading"
          subHeadingID="config.general.blobs_storage.description"
          value={general.blobsStorage ?? GQL.BlobsStorageType.Database}
          onChange={(v) =>
            saveGeneral({ blobsStorage: v as GQL.BlobsStorageType })
          }
        >
          {Object.values(GQL.BlobsStorageType).map((q) => (
            <option key={q} value={q}>
              {intl.formatMessage({ id: blobStorageTypeToID(q) })}
            </option>
          ))}
        </SelectSetting>
        <StringSetting
          id="blobs-path"
          headingID="config.general.blobs_path.heading"
          subHeadingID="config.general.blobs_path.description"
          value={general.blobsPath ?? ""}
          onChange={(v) => saveGeneral({ blobsPath: v })}
        />
      </SettingSection>

      <SettingSection advanced headingID="config.general.hashing">
        <BooleanSetting
          id="calculate-md5-and-ohash"
          headingID="config.general.calculate_md5_and_ohash_label"
          subHeadingID="config.general.calculate_md5_and_ohash_desc"
          checked={general.calculateMD5 ?? false}
          onChange={(v) => saveGeneral({ calculateMD5: v })}
        />

        <SelectSetting
          id="generated_file_naming_hash"
          headingID="config.general.generated_file_naming_hash_head"
          subHeadingID="config.general.generated_file_naming_hash_desc"
          value={namingHashToString(
            general.videoFileNamingAlgorithm ?? undefined
          )}
          onChange={(v) =>
            saveGeneral({ videoFileNamingAlgorithm: translateNamingHash(v) })
          }
        >
          {namingHashAlgorithms.map((q) => (
            <option key={q} value={q}>
              {q}
            </option>
          ))}
        </SelectSetting>
      </SettingSection>

      <SettingSection headingID="config.system.transcoding">
        <SelectSetting
          advanced
          id="transcode-size"
          headingID="config.general.maximum_transcode_size_head"
          subHeadingID="config.general.maximum_transcode_size_desc"
          onChange={(v) =>
            saveGeneral({ maxTranscodeSize: translateQuality(v) })
          }
          value={resolutionToString(general.maxTranscodeSize ?? undefined)}
        >
          {transcodeQualities.map((q) => (
            <option key={q} value={q}>
              {q}
            </option>
          ))}
        </SelectSetting>

        <SelectSetting
          id="streaming-transcode-size"
          headingID="config.general.maximum_streaming_transcode_size_head"
          subHeadingID="config.general.maximum_streaming_transcode_size_desc"
          onChange={(v) =>
            saveGeneral({ maxStreamingTranscodeSize: translateQuality(v) })
          }
          value={resolutionToString(
            general.maxStreamingTranscodeSize ?? undefined
          )}
        >
          {transcodeQualities.map((q) => (
            <option key={q} value={q}>
              {q}
            </option>
          ))}
        </SelectSetting>

        <BooleanSetting
          id="hardware-encoding"
          headingID="config.general.ffmpeg.hardware_acceleration.heading"
          subHeadingID="config.general.ffmpeg.hardware_acceleration.desc"
          checked={general.transcodeHardwareAcceleration ?? false}
          onChange={(v) => saveGeneral({ transcodeHardwareAcceleration: v })}
        />

        <StringListSetting
          advanced
          id="transcode-input-args"
          headingID="config.general.ffmpeg.transcode.input_args.heading"
          subHeadingID="config.general.ffmpeg.transcode.input_args.desc"
          onChange={(v) => saveGeneral({ transcodeInputArgs: v })}
          value={general.transcodeInputArgs ?? []}
        />
        <StringListSetting
          advanced
          id="transcode-output-args"
          headingID="config.general.ffmpeg.transcode.output_args.heading"
          subHeadingID="config.general.ffmpeg.transcode.output_args.desc"
          onChange={(v) => saveGeneral({ transcodeOutputArgs: v })}
          value={general.transcodeOutputArgs ?? []}
        />

        <StringListSetting
          advanced
          id="live-transcode-input-args"
          headingID="config.general.ffmpeg.live_transcode.input_args.heading"
          subHeadingID="config.general.ffmpeg.live_transcode.input_args.desc"
          onChange={(v) => saveGeneral({ liveTranscodeInputArgs: v })}
          value={general.liveTranscodeInputArgs ?? []}
        />
        <StringListSetting
          advanced
          id="live-transcode-output-args"
          headingID="config.general.ffmpeg.live_transcode.output_args.heading"
          subHeadingID="config.general.ffmpeg.live_transcode.output_args.desc"
          onChange={(v) => saveGeneral({ liveTranscodeOutputArgs: v })}
          value={general.liveTranscodeOutputArgs ?? []}
        />
      </SettingSection>

      <SettingSection headingID="config.general.parallel_scan_head">
        <NumberSetting
          id="parallel-tasks"
          headingID="config.general.number_of_parallel_task_for_scan_generation_head"
          subHeadingID="config.general.number_of_parallel_task_for_scan_generation_desc"
          value={general.parallelTasks ?? undefined}
          onChange={(v) => saveGeneral({ parallelTasks: v })}
        />
      </SettingSection>

      <SettingSection headingID="config.general.preview_generation">
        <SelectSetting
          id="scene-gen-preview-preset"
          headingID="dialogs.scene_gen.preview_preset_head"
          subHeadingID="dialogs.scene_gen.preview_preset_desc"
          value={general.previewPreset ?? undefined}
          onChange={(v) =>
            saveGeneral({
              previewPreset: (v as GQL.PreviewPreset) ?? undefined,
            })
          }
        >
          {Object.keys(GQL.PreviewPreset).map((p) => (
            <option value={p.toLowerCase()} key={p}>
              {p}
            </option>
          ))}
        </SelectSetting>

        <BooleanSetting
          id="preview-include-audio"
          headingID="config.general.include_audio_head"
          subHeadingID="config.general.include_audio_desc"
          checked={general.previewAudio ?? false}
          onChange={(v) => saveGeneral({ previewAudio: v })}
        />

        <ModalSetting<VideoPreviewSettingsInput>
          id="video-preview-settings"
          headingID="dialogs.scene_gen.preview_generation_options"
          value={{
            previewExcludeEnd: general.previewExcludeEnd,
            previewExcludeStart: general.previewExcludeStart,
            previewSegmentDuration: general.previewSegmentDuration,
            previewSegments: general.previewSegments,
          }}
          onChange={(v) => saveGeneral(v)}
          renderField={(value, setValue) => (
            <VideoPreviewInput value={value ?? {}} setValue={setValue} />
          )}
          renderValue={() => {
            return <></>;
          }}
        />
      </SettingSection>

      <IntelGenerationSettings />

      <SettingSection headingID="config.general.native_generation">
        <BooleanSetting
          id="native-generation"
          headingID="config.general.native_generation_enabled"
          subHeading={
            nativeBackend
              ? intl.formatMessage(
                  { id: "config.general.native_generation_enabled_desc" },
                  { backend: nativeBackend }
                )
              : intl.formatMessage({
                  id: "config.general.native_generation_unavailable",
                })
          }
          disabled={!nativeBackend}
          checked={general.nativeGeneration ?? false}
          onChange={(v) => saveGeneral({ nativeGeneration: v })}
        />
        <BooleanSetting
          id="native-phash-generation"
          headingID="config.general.native_phash_generation"
          subHeadingID="config.general.native_phash_generation_desc"
          disabled={!nativeBackend || !general.nativeGeneration}
          checked={general.nativePhashGeneration ?? true}
          onChange={(v) => saveGeneral({ nativePhashGeneration: v })}
        />
        <BooleanSetting
          advanced
          id="native-marker-generation"
          headingID="config.general.native_marker_generation"
          subHeadingID="config.general.native_marker_generation_desc"
          disabled={!nativeBackend || !general.nativeGeneration}
          checked={general.nativeMarkerGeneration ?? false}
          onChange={(v) => saveGeneral({ nativeMarkerGeneration: v })}
        />
      </SettingSection>

      <SettingSection headingID="config.general.heatmap_generation">
        <BooleanSetting
          id="heatmap-draw-range"
          headingID="config.general.funscript_heatmap_draw_range"
          subHeadingID="config.general.funscript_heatmap_draw_range_desc"
          checked={general.drawFunscriptHeatmapRange ?? true}
          onChange={(v) => saveGeneral({ drawFunscriptHeatmapRange: v })}
        />
      </SettingSection>

      <SettingSection headingID="config.general.logging">
        <StringSetting
          headingID="config.general.auth.log_file"
          subHeadingID="config.general.auth.log_file_desc"
          value={general.logFile ?? undefined}
          onChange={(v) => saveGeneral({ logFile: v })}
        />

        <BooleanSetting
          id="log-terminal"
          headingID="config.general.auth.log_to_terminal"
          subHeadingID="config.general.auth.log_to_terminal_desc"
          checked={general.logOut ?? false}
          onChange={(v) => saveGeneral({ logOut: v })}
        />

        <SelectSetting
          id="log-level"
          headingID="config.logs.log_level"
          onChange={(v) => saveGeneral({ logLevel: v })}
          value={general.logLevel ?? undefined}
        >
          {["Trace", "Debug", "Info", "Warning", "Error"].map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </SelectSetting>

        <BooleanSetting
          id="log-http"
          headingID="config.general.auth.log_http"
          subHeadingID="config.general.auth.log_http_desc"
          checked={general.logAccess ?? false}
          onChange={(v) => saveGeneral({ logAccess: v })}
        />

        <NumberSetting
          id="log-file-max-size"
          headingID="config.general.auth.log_file_max_size"
          subHeadingID="config.general.auth.log_file_max_size_desc"
          value={general.logFileMaxSize ?? 10}
          onChange={(v) => saveGeneral({ logFileMaxSize: v })}
        />
      </SettingSection>
    </>
  );
};
