import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { api } from './client';
import type { AuditFilter, CreateExportRequest, Export, ExportFormat } from './types';

export const queryKeys = {
  me: ['me'] as const,
  preferences: ['preferences'] as const,
  exports: ['exports'] as const,
  search: (q: string) => ['search', q] as const,
  children: (id: string) => ['children', id] as const,
  audit: (filter: AuditFilter) => ['audit', filter] as const,
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
