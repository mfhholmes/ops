package lepton

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ebs"
	"github.com/aws/aws-sdk-go/service/ec2"
)

var (
	errGettingAWSVolumeService = func(err error) error { return fmt.Errorf("get volume service: %v", err) }
)

// CreateVolume creates a snapshot and use it to create a volume
func (a *AWS) CreateVolume(config *Config, name, data, size, provider string) (NanosVolume, error) {
	var vol NanosVolume

	compute, err := a.getEc2Service(config)
	if err != nil {
		return vol, err
	}

	// Create volume
	localVolume, err := CreateLocalVolume(config, name, data, size, provider)
	if err != nil {
		return vol, fmt.Errorf("create local volume: %v", err)
	}

	config.CloudConfig.ImageName = localVolume.Name

	err = a.Storage.CopyToBucket(config, localVolume.Path)
	if err != nil {
		return vol, fmt.Errorf("copy volume archive to aws bucket: %v", err)
	}

	bucket := config.CloudConfig.BucketName
	key := localVolume.Name

	input := &ec2.ImportSnapshotInput{
		Description: aws.String("name"),
		DiskContainer: &ec2.SnapshotDiskContainer{
			Description: aws.String("snapshot imported"),
			Format:      aws.String("raw"),
			UserBucket: &ec2.UserBucket{
				S3Bucket: aws.String(bucket),
				S3Key:    aws.String(key),
			},
		},
	}

	res, err := compute.ImportSnapshot(input)
	if err != nil {
		return vol, fmt.Errorf("import snapshot: %v", err)
	}

	snapshotID, err := a.waitSnapshotToBeReady(config, res.ImportTaskId)
	if err != nil {
		return vol, err
	}

	// delete the tmp s3 volume
	err = a.Storage.DeleteFromBucket(config, key)
	if err != nil {
		return vol, err
	}

	// Create tags to assign to the volume
	tags, _ := parseToAWSTags(config.RunConfig.Tags, name)

	// Create volume from snapshot
	createVolumeInput := &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String(config.CloudConfig.Zone + "c"),
		SnapshotId:       snapshotID,
		TagSpecifications: []*ec2.TagSpecification{
			{
				ResourceType: aws.String("volume"),
				Tags:         tags,
			},
		},
	}
	_, err = compute.CreateVolume(createVolumeInput)
	if err != nil {
		return vol, fmt.Errorf("create aws volume: %v", err)
	}

	return vol, nil
}

// GetAllVolumes finds and returns all volumes
func (a *AWS) GetAllVolumes(config *Config) (*[]NanosVolume, error) {
	vols := &[]NanosVolume{}

	compute, err := a.getEc2Service(config)
	if err != nil {
		return nil, err
	}

	input := &ec2.DescribeVolumesInput{}
	output, err := compute.DescribeVolumes(input)
	if err != nil {
		return nil, err
	}

	for _, volume := range output.Volumes {
		var name string
		var attachments []string

		for _, tag := range volume.Tags {
			if *tag.Key == "Name" {
				name = *tag.Value
			}
		}

		for _, att := range volume.Attachments {
			attachments = append(attachments, *att.InstanceId)
		}

		vol := NanosVolume{
			ID:         *volume.VolumeId,
			Name:       name,
			Status:     *volume.State,
			Size:       strconv.Itoa(int(*volume.Size)),
			Path:       "",
			CreatedAt:  volume.CreateTime.String(),
			AttachedTo: strings.Join(attachments, ";"),
		}

		*vols = append(*vols, vol)
	}

	return vols, nil
}

// DeleteVolume deletes a volume
func (a *AWS) DeleteVolume(config *Config, name string) error {
	compute, err := a.getEc2Service(config)
	if err != nil {
		return err
	}

	input := &ec2.DeleteVolumeInput{
		VolumeId: aws.String(name),
	}
	_, err = compute.DeleteVolume(input)
	if err != nil {
		return err
	}

	return nil
}

// AttachVolume attaches a volume to an instance
func (a *AWS) AttachVolume(config *Config, image, name, mount string) error {
	compute, err := a.getEc2Service(config)
	if err != nil {
		return err
	}

	input := &ec2.AttachVolumeInput{
		Device:     aws.String("/dev/sdf"),
		InstanceId: aws.String(image),
		VolumeId:   aws.String(name),
	}
	_, err = compute.AttachVolume(input)
	if err != nil {
		return err
	}

	return nil
}

// DetachVolume detachs a volume from an instance
func (a *AWS) DetachVolume(config *Config, image, name string) error {
	compute, err := a.getEc2Service(config)
	if err != nil {
		return err
	}

	input := &ec2.DetachVolumeInput{
		Device:     aws.String("/dev/sdf"),
		InstanceId: aws.String(image),
		VolumeId:   aws.String(name),
	}
	_, err = compute.DetachVolume(input)
	if err != nil {
		return err
	}

	return nil
}

func (a *AWS) getVolumeService(config *Config) (*ebs.EBS, error) {
	if a.volumeService != nil {
		return a.volumeService, nil
	}

	sess, err := a.getAWSSession(config)
	if err != nil {
		return nil, err
	}

	a.volumeService = ebs.New(sess)

	return a.volumeService, nil
}
