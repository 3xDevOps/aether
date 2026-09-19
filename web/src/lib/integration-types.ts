import type { EvidencePacket } from '@/lib/types'

/** The integration RPCs mirror internal/protocol/integration.go exactly. */
export type CandidateState =
  | 'preparing'
  | 'conflicted'
  | 'frozen'
  | 'unavailable'
  | 'deleting'
  | 'expired'

export type VerificationStatus =
  | 'running'
  | 'passed'
  | 'failed'
  | 'timed_out'
  | 'cancelled'
  | 'error'
  | 'source_changed'

export type DeliveryAction = 'update_ref' | 'proposal'
export type DeliveryState = 'pending' | 'approved' | 'denied' | 'delivering' | 'delivered'
export type DeliveryResult = 'landed' | 'proposed'

/** The protocol snapshot includes availability/origin fields not present in
 * the older run evidence wire type. */
export interface CandidatePacket extends EvidencePacket {
  origin: { kind: string; id: string }
  owner_id?: string
  availability: 'available' | 'expired' | string
  expired_at?: string
}

export interface SubmissionRef {
  workspace_id: string
  run_id: string
  evidence_ref: string
  retained_revision: string
}

export interface CandidateInput {
  submission: SubmissionRef
  base_revision: string
  packet: CandidatePacket
  transcript_id?: string
  transcript_checksum?: string
}

export interface CandidateMutation {
  actor_key: string
  operation: string
  idempotency_key: string
  digest: string
  result_id: string
}

export interface Verification {
  verification_id: string
  candidate_revision: string
  argv: string[]
  image: string
  observed_image?: string
  user?: string
  working_dir?: string
  timeout_seconds?: number
  cpu_limit?: number
  memory_limit_bytes?: number
  environment_sha256?: string
  setup_script_sha256?: string
  status: VerificationStatus
  exit_code?: number
  output?: string
  output_truncated?: boolean
  error?: string
  created_at: string
  finished_at?: string
  expires_at: string
  creation_key?: string
  container_id?: string
}

export interface DeliveryRequest {
  request_id: string
  request_version: number
  candidate_revision: string
  verification_ids: string[]
  target_ref: string
  expected_target_revision: string
  action: DeliveryAction
  state: DeliveryState
  requested_by: string
  decided_by?: string
  created_at: string
  expires_at: string
  decided_at?: string
}

export interface DeliveryReceipt {
  receipt_id: string
  request_id: string
  candidate_revision: string
  target_ref: string
  previous_revision: string
  action: DeliveryAction
  result: DeliveryResult
  proposal_ref?: string
  created_at: string
}

export interface Candidate {
  candidate_id: string
  workspace_id: string
  mission_id?: string
  submissions: SubmissionRef[]
  required_sources?: string[]
  inputs: CandidateInput[]
  target_ref: string
  expected_target_revision: string
  candidate_revision?: string
  state: CandidateState
  conflicts?: string[]
  applied_inputs: number
  verifications: Verification[]
  delivery_request?: DeliveryRequest
  delivery_receipt?: DeliveryReceipt
  mutations: CandidateMutation[]
  created_at: string
  expires_at: string
  error?: string
  version: number
}

export interface CandidateSummary {
  candidate_id: string
  workspace_id: string
  state: CandidateState
  candidate_revision?: string
  target_ref: string
  expected_target_revision: string
  delivery_request?: DeliveryRequest
  delivery_receipt?: DeliveryReceipt
  created_at: string
  expires_at: string
}

export interface IntegrationPrepareParams {
  workspace_id: string
  mission_id?: string
  submissions: SubmissionRef[]
  target_ref: string
  expected_target_revision: string
  required_sources?: string[]
  idempotency_key: string
}
export interface IntegrationPrepareResult { candidate: Candidate }

export interface IntegrationShowParams { workspace_id: string; candidate_id: string }
export interface IntegrationShowResult { candidate: Candidate }

export interface IntegrationListParams { workspace_id: string; limit?: number }
export interface IntegrationListResult { candidates: CandidateSummary[] }

export interface CandidateResolution { path: string; content?: string; delete?: boolean }
export interface IntegrationResolveParams {
  workspace_id: string
  candidate_id: string
  expected_version: number
  files: CandidateResolution[]
  idempotency_key: string
}
export interface IntegrationResolveResult { candidate: Candidate }

export interface IntegrationVerifyParams {
  workspace_id: string
  candidate_id: string
  candidate_revision: string
  argv: string[]
  timeout_seconds: number
  idempotency_key: string
}
export interface IntegrationVerifyResult { candidate: Candidate }

export interface IntegrationRequestDeliveryParams {
  workspace_id: string
  candidate_id: string
  candidate_revision: string
  verification_ids: string[]
  action: DeliveryAction
  idempotency_key: string
}
export interface IntegrationRequestDeliveryResult { candidate: Candidate }

export interface IntegrationDecideParams {
  workspace_id: string
  candidate_id: string
  request_id: string
  request_version: number
  approve: boolean
}
export interface IntegrationDecideResult { candidate: Candidate }

export interface IntegrationDeliverParams {
  workspace_id: string
  candidate_id: string
  request_id: string
  request_version: number
}
export interface IntegrationDeliverResult { candidate: Candidate }

export interface IntegrationPatchResult {
  patch: string
  truncated: boolean
}

export interface IntegrationDeleteParams { workspace_id: string; candidate_id: string }
export interface IntegrationDeleteResult {}
