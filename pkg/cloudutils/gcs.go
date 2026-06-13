package cloudutils

import (
	"context"
	"fmt"
	"io"
	"os"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

func UploadObjectToGCS(bucketName, filePath, objectName string) error {
	// Create a context
	ctx := context.Background()

	// Initialize the GCS client
	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create GCS client: %w", err)
	}
	defer client.Close()

	// Open the file to be uploaded
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer file.Close()

	// Get a handle for the bucket and object
	bucket := client.Bucket(bucketName)
	object := bucket.Object(objectName)

	// Create a writer to upload the file
	writer := object.NewWriter(ctx)
	defer writer.Close()

	// Copy the file's content to GCS
	for {

		buf := make([]byte, 1024*1024)
		n, err := file.Read(buf)
		if err != nil && err != io.EOF {
			return fmt.Errorf("unable to read file : %w", err)

		}
		if n == 0 {
			break
		}
		if _, err := writer.Write(buf); err != nil {
			return fmt.Errorf("failed to upload file to GCS: %w", err)
		}
	}

	fmt.Printf("File %s uploaded to bucket %s with object name %s\n", filePath, bucketName, objectName)
	return nil
}

func DownloadObjectFromGCS(bucketName, objectName, filePath string) error {
	// Create a context
	ctx := context.Background()

	// Initialize the GCS client
	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create GCS client: %w", err)
	}
	defer client.Close()

	// Get a handle for the bucket and object
	bucket := client.Bucket(bucketName)
	object := bucket.Object(objectName)

	// Open a reader for the object
	reader, err := object.NewReader(ctx)
	if err != nil {
		return fmt.Errorf("failed to open object %s: %w", objectName, err)
	}
	defer reader.Close()

	// Create the destination file
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filePath, err)
	}
	defer file.Close()

	// Copy the object's content to the file
	if _, err := io.Copy(file, reader); err != nil {
		return fmt.Errorf("failed to download file from GCS: %w", err)
	}

	fmt.Printf("File downloaded from bucket %s with object name %s to %s\n", bucketName, objectName, filePath)
	return nil
}

func DeleteObjectFromGCS(bucketName, objectName string) error {
	// Create a context
	ctx := context.Background()

	// Initialize the GCS client
	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create GCS client: %w", err)
	}
	defer client.Close()

	// Get a handle for the bucket and object
	bucket := client.Bucket(bucketName)
	object := bucket.Object(objectName)

	// Delete the object
	err = object.Delete(ctx)
	if err != nil {
		if gerr, ok := err.(*googleapi.Error); ok && gerr.Code == 404 {

			fmt.Printf(" object %s not found in  bucket %s", objectName, bucketName)
			return nil
		}
		return fmt.Errorf("failed to delete object %s from bucket %s: %w", objectName, bucketName, err)
	}

	fmt.Printf("Object %s deleted from bucket %s successfully\n", objectName, bucketName)
	return nil
}
