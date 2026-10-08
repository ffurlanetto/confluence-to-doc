import {
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';

import { api } from './client';
import type { AuditFilter, CreateExportRequest, Export, ExportFormat, ExportStatus } from './types';

export const queryKeys = {
  me: ['me'] as const,
  preferences: ['preferences'] as const,
  exports: ['exports'] as const,
  search: (q: string) => ['search', q] as const,
  children: (id: string) => ['children', id] as const,
  audit: (filter: AuditFilter) => ['audit', filter] as const,
  notifications: ['notifications'] as const,
  adminQueue: (statuses: ExportStatus[]) => ['admin', 'queue', statuses] as const,
  adminUsers: (q: string) => ['admin', 'users', q] as const,
  usage: (days: number) => ['admin', 'usage', days] as const,
  template: ['admin', 'template'] as const,
};

export const useMe = () => useQuery({ queryKey: queryKeys.me, queryFn: api.me, staleTime: Infinity });

export const usePreferences = () => useQuery({ queryKey: queryKeys.preferences, queryFn: api.preferences });

const hasActiveExport = (exports: Export[] | undefined) =>
  exports?.some((e) => e.status === 'queued' || e.status === 'running') ?? false;

/** Lists exports, polling while some are still being generated. */
export const useExports = () =>
  useQuery({
    queryKey: queryKeys.exports,
    queryFn: api.exports,
    refetchInterval: (query) => (hasActiveExport(query.state.data) ? 2000 : 30000),
  });

export const useSearchPages = (q: string) =>
  useQuery({
    queryKey: queryKeys.search(q),
    queryFn: () => api.searchPages(q),
    enabled: q.trim().length >= 2,
    staleTime: 30_000,
  });

export const useChildren = (pageId: string, enabled: boolean) =>
  useQuery({
    queryKey: queryKeys.children(pageId),
    queryFn: () => api.children(pageId),
    enabled,
    staleTime: 60_000,
  });

export const useCreateExport = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: CreateExportRequest) => api.createExport(req),
    onSuccess: () => qc.invalidateQueries({ queryKey: queryKeys.exports }),
  });
};

export const useDeleteExport = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteExport(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: queryKeys.exports }),
  });
};

export const useSetPat = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (token: string) => api.setPat(token),
    onSuccess: () => qc.invalidateQueries({ queryKey: queryKeys.preferences }),
  });
};

export const useDeletePat = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.deletePat,
    onSuccess: () => qc.invalidateQueries({ queryKey: queryKeys.preferences }),
  });
};

export const useSetDefaultFormat = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (f: ExportFormat) => api.setDefaultFormat(f),
    onSuccess: (prefs) => qc.setQueryData(queryKeys.preferences, prefs),
  });
};

/** Pages through the audit trail, newest first. */
export const useAuditEvents = (filter: AuditFilter) =>
  useInfiniteQuery({
    queryKey: queryKeys.audit(filter),
    queryFn: ({ pageParam }) => api.auditEvents(filter, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });

/** Erases the user's account and data; the session ends with it. */
export const useDeleteAccount = () => useMutation({ mutationFn: api.deleteAccount });

/** The user's notifications, refreshed every 30 seconds for the unread badge. */
export const useNotifications = () =>
  useQuery({ queryKey: queryKeys.notifications, queryFn: api.notifications, refetchInterval: 30_000 });

export const useMarkNotificationsRead = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.markNotificationsRead,
    onSuccess: () => qc.invalidateQueries({ queryKey: queryKeys.notifications }),
  });
};

export const useSetNotificationPreferences = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ email, exports }: { email: boolean; exports: boolean }) =>
      api.setNotificationPreferences(email, exports),
    onSuccess: (prefs) => qc.setQueryData(queryKeys.preferences, prefs),
  });
};

export const useSetTeamsWebhook = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (url: string) => api.setTeamsWebhook(url),
    onSuccess: (prefs) => qc.setQueryData(queryKeys.preferences, prefs),
  });
};

export const useDeleteTeamsWebhook = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.deleteTeamsWebhook,
    onSuccess: () => qc.invalidateQueries({ queryKey: queryKeys.preferences }),
  });
};

export const useTestTeams = () => useMutation({ mutationFn: api.testTeams });

// ------------------------------------------------------------ administration

/** Exports of every user; refreshed every 5 seconds while the page is open. */
export const useAdminQueue = (statuses: ExportStatus[]) =>
  useQuery({
    queryKey: queryKeys.adminQueue(statuses),
    queryFn: () => api.adminQueue(statuses),
    refetchInterval: 5000,
  });

const useAdminMutation = <A>(fn: (arg: A) => Promise<unknown>) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin'] }),
  });
};

export const useCancelExport = () => useAdminMutation((exportId: string) => api.cancelExport(exportId));
export const useRetryExport = () => useAdminMutation((exportId: string) => api.retryExport(exportId));

export const useAdminUsers = (q: string) =>
  useQuery({ queryKey: queryKeys.adminUsers(q), queryFn: () => api.adminUsers(q) });

export const useBlockUser = () =>
  useAdminMutation(({ userId, reason }: { userId: string; reason: string }) => api.blockUser(userId, reason));
export const useUnblockUser = () => useAdminMutation((userId: string) => api.unblockUser(userId));

/** Usage over the last days; the previous figures stay shown while a new period loads. */
export const useUsage = (days: number) =>
  useQuery({
    queryKey: queryKeys.usage(days),
    queryFn: () => api.usage(days),
    placeholderData: keepPreviousData,
  });

export const useTemplate = () => useQuery({ queryKey: queryKeys.template, queryFn: api.template });

/** Uploading or removing the template also changes what the preferences show. */
const useTemplateMutation = <A>(fn: (arg: A) => Promise<unknown>) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: queryKeys.template });
      await qc.invalidateQueries({ queryKey: queryKeys.preferences });
    },
  });
};

export const useUploadTemplate = () => useTemplateMutation((file: File) => api.uploadTemplate(file));
export const useDeleteTemplate = () => useTemplateMutation<undefined>(() => api.deleteTemplate());
