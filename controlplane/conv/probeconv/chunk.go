// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package probeconv

import (
	"go.opentelemetry.io/collector/custom/controlplane/model"
	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

func ChunkUploadFromProto(req *controlplanev1.ChunkedTaskResult) *model.ChunkUpload {
	if req == nil {
		return nil
	}
	return &model.ChunkUpload{
		TaskID:        req.GetTaskId(),
		UploadID:      req.GetUploadId(),
		ChunkIndex:    req.GetChunkIndex(),
		TotalChunks:   req.GetTotalChunks(),
		ChunkData:     req.GetChunkData(),
		ChunkChecksum: req.GetChunkChecksum(),
		IsLastChunk:   req.GetIsLastChunk(),
	}
}

func ChunkUploadToProto(req *model.ChunkUpload) *controlplanev1.ChunkedTaskResult {
	if req == nil {
		return nil
	}
	return &controlplanev1.ChunkedTaskResult{
		TaskId:        req.TaskID,
		UploadId:      req.UploadID,
		ChunkIndex:    req.ChunkIndex,
		TotalChunks:   req.TotalChunks,
		ChunkData:     req.ChunkData,
		ChunkChecksum: req.ChunkChecksum,
		IsLastChunk:   req.IsLastChunk,
	}
}

func ChunkUploadResponseFromProto(resp *controlplanev1.ChunkedUploadResponse) *model.ChunkUploadResponse {
	if resp == nil {
		return nil
	}
	return &model.ChunkUploadResponse{
		UploadID:           resp.GetUploadId(),
		ReceivedChunkIndex: resp.GetReceivedChunkIndex(),
		Status:             chunkUploadStatusFromProto(resp.GetStatus()),
		ErrorMessage:       resp.GetErrorMessage(),
	}
}

func ChunkUploadResponseToProto(resp *model.ChunkUploadResponse) *controlplanev1.ChunkedUploadResponse {
	if resp == nil {
		return nil
	}
	return &controlplanev1.ChunkedUploadResponse{
		UploadId:           resp.UploadID,
		ReceivedChunkIndex: resp.ReceivedChunkIndex,
		Status:             chunkUploadStatusToProto(resp.Status),
		ErrorMessage:       resp.ErrorMessage,
	}
}

func chunkUploadStatusFromProto(st controlplanev1.ChunkUploadStatus) model.ChunkUploadStatus {
	switch st {
	case controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_CHUNK_RECEIVED:
		return model.ChunkUploadStatusChunkReceived
	case controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_COMPLETE:
		return model.ChunkUploadStatusUploadComplete
	case controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_CHECKSUM_MISMATCH:
		return model.ChunkUploadStatusChecksumMismatch
	case controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_FAILED:
		return model.ChunkUploadStatusUploadFailed
	default:
		return model.ChunkUploadStatusUnspecified
	}
}

func chunkUploadStatusToProto(st model.ChunkUploadStatus) controlplanev1.ChunkUploadStatus {
	switch st {
	case model.ChunkUploadStatusChunkReceived:
		return controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_CHUNK_RECEIVED
	case model.ChunkUploadStatusUploadComplete:
		return controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_COMPLETE
	case model.ChunkUploadStatusChecksumMismatch:
		return controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_CHECKSUM_MISMATCH
	case model.ChunkUploadStatusUploadFailed:
		return controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_FAILED
	default:
		return controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UNSPECIFIED
	}
}
