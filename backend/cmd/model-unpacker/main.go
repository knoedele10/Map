// model-unpacker verifies the offline model volumes and extracts their tar stream.
package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type bundleManifest struct {
	Version    string      `json:"version"`
	TotalBytes int64       `json:"totalBytes"`
	Parts      []bundlePart `json:"parts"`
}

type bundlePart struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func main() {
	manifestPath := flag.String("manifest", "", "model bundle manifest JSON")
	sourceDir := flag.String("source", "", "directory containing the model volumes")
	targetDir := flag.String("target", "", "Ollama runtime directory to receive models/")
	flag.Parse()
	if err := unpack(*manifestPath, *sourceDir, *targetDir); err != nil {
		fmt.Fprintln(os.Stderr, "模型安装失败：", err)
		os.Exit(1)
	}
}

func unpack(manifestPath, sourceDir, targetDir string) error {
	if manifestPath == "" || sourceDir == "" || targetDir == "" {
		return errors.New("必须指定 --manifest、--source 和 --target")
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("读取校验清单：%w", err)
	}
	var manifest bundleManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("解析校验清单：%w", err)
	}
	if manifest.Version != "1.0.2" || len(manifest.Parts) != 4 || manifest.TotalBytes <= 0 {
		return errors.New("模型包版本、分卷数量或总大小不正确")
	}

	base := "Shuji-Rongbao-Offline-Models-" + manifest.Version + ".part"
	var partPaths []string
	var declaredTotal int64
	for i, part := range manifest.Parts {
		wantName := fmt.Sprintf("%s%02d.bin", base, i+1)
		if part.Name != wantName || filepath.Base(part.Name) != part.Name || part.Bytes <= 0 {
			return fmt.Errorf("模型分卷清单无效：%q", part.Name)
		}
		digest, err := hex.DecodeString(part.SHA256)
		if err != nil || len(digest) != sha256.Size {
			return fmt.Errorf("分卷 SHA-256 格式无效：%s", part.Name)
		}
		partPath := filepath.Join(sourceDir, part.Name)
		if err := verifyPart(partPath, part); err != nil {
			return err
		}
		partPaths = append(partPaths, partPath)
		declaredTotal += part.Bytes
	}
	if declaredTotal != manifest.TotalBytes {
		return errors.New("模型包总大小与分卷清单不一致")
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("创建模型目录：%w", err)
	}
	readers := make([]io.Reader, 0, len(partPaths))
	files := make([]*os.File, 0, len(partPaths))
	for _, partPath := range partPaths {
		file, err := os.Open(partPath)
		if err != nil {
			closeFiles(files)
			return fmt.Errorf("打开模型分卷：%w", err)
		}
		files = append(files, file)
		readers = append(readers, file)
	}
	err = extractTar(io.MultiReader(readers...), targetDir)
	closeFiles(files)
	if err != nil {
		return err
	}

	modelRoot := filepath.Join(targetDir, "models")
	for _, required := range []string{
		filepath.Join("manifests", "registry.ollama.ai", "library", "qwen2.5vl", "7b"),
		filepath.Join("manifests", "registry.ollama.ai", "library", "bge-m3", "latest"),
	} {
		if _, err := os.Stat(filepath.Join(modelRoot, required)); err != nil {
			return fmt.Errorf("解压后缺少 Ollama 模型清单 %s：%w", required, err)
		}
	}
	fmt.Println("离线模型已校验并安装。")
	return nil
}

func verifyPart(path string, part bundlePart) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("缺少模型分卷 %s：%w", part.Name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != part.Bytes {
		return fmt.Errorf("模型分卷大小不正确：%s", part.Name)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("校验模型分卷 %s：%w", part.Name, err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != part.SHA256 {
		return fmt.Errorf("模型分卷校验失败：%s", part.Name)
	}
	return nil
}

func extractTar(input io.Reader, targetDir string) error {
	root, err := filepath.Abs(targetDir)
	if err != nil {
		return err
	}
	reader := tar.NewReader(input)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("读取模型归档：%w", err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(header.Name), "./")
		if name == "" || name == "." {
			continue
		}
		if name != "models" && name != "licenses" && !strings.HasPrefix(name, "models/") && !strings.HasPrefix(name, "licenses/") {
			return fmt.Errorf("拒绝模型包中的未知路径：%s", name)
		}
		clean := filepath.Clean(filepath.FromSlash(name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("拒绝越界模型路径：%s", name)
		}
		destination := filepath.Join(root, clean)
		relative, err := filepath.Rel(root, destination)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("拒绝越界模型路径：%s", name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return fmt.Errorf("创建模型文件 %s：%w", name, err)
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("写入模型文件 %s：%w", name, copyErr)
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("不支持模型归档文件类型 %d：%s", header.Typeflag, name)
		}
	}
}

func closeFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}
