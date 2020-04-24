package main

import (
	"encoding/json"
	"os"

	cstorage "github.com/containers/storage"
	"github.com/cri-o/cri-o/internal/pkg/criocli"
	"github.com/cri-o/cri-o/internal/pkg/storage"
	"github.com/cri-o/cri-o/internal/version"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

var wipeCommand = &cli.Command{
	Name:   "wipe",
	Usage:  "wipe CRI-O's container and image storage",
	Action: crioWipe,
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "force",
			Aliases: []string{"f"},
			Usage:   "force wipe by skipping the version check",
		},
	},
}

func crioWipe(c *cli.Context) error {
	_, config, err := criocli.GetConfigFromContext(c)
	if err != nil {
		return err
	}

	shouldWipeImages := true
	shouldWipeContainers := true
	// First, check if we need to upgrade at all
	if !c.IsSet("force") {
		// there are two locations we check before wiping:
		// one in a temporary directory. This is to check whether the node has rebooted.
		// if so, we should remove containers
		shouldWipeContainers, err = version.ShouldCrioWipe(config.VersionFile)
		if err != nil {
			logrus.Infof("%v: triggering wipe of containers", err.Error())
		}
		// another is needed in a persistent directory. This is to check whether we've upgraded
		// if we've upgraded, we should wipe images
		shouldWipeImages, err = version.ShouldCrioWipe(config.VersionFilePersist)
		if err != nil {
			logrus.Infof("%v: triggering wipe of images", err.Error())
		}
	}

	// if we should not wipe, exit with no error
	if !shouldWipeContainers {
		// we should not wipe images without wiping containers
		// in a future release, we should wipe both container and images if only shouldWipeImages is true.
		// However, now, we cannot expect users to have version-file-persist after having upgraded
		// to this version. Skip the wipe, for now, and log about it.
		if shouldWipeImages {
			logrus.Infof("legacy version-file path found, but new version-file-persist path not. Skipping wipe")
		}
		logrus.Infof("version unchanged and node not rebooted; no wipe needed")
		return nil
	}

	store, err := config.GetStore()
	if err != nil {
		return err
	}

	cstore := ContainerStore{store}
	if err := cstore.wipeCrio(shouldWipeImages); err != nil {
		return err
	}

	return nil
}

type ContainerStore struct {
	store cstorage.Store
}

func (c ContainerStore) wipeCrio(shouldWipeImages bool) error {
	crioContainers, crioImages, err := c.getCrioContainersAndImages()
	if err != nil {
		return err
	}
	for _, id := range crioContainers {
		c.deleteContainer(id)
	}
	if shouldWipeImages {
		for _, id := range crioImages {
			c.deleteImage(id)
		}
	}
	return nil
}

func (c ContainerStore) getCrioContainersAndImages() (crioContainers, crioImages []string, err error) {
	containers, err := c.store.Containers()
	if err != nil {
		if os.IsNotExist(errors.Cause(err)) {
			return crioContainers, crioImages, err
		}
		logrus.Errorf("could not read containers and sandboxes: %v", err)
	}

	for i := range containers {
		id := containers[i].ID
		metadataString, err := c.store.Metadata(id)
		if err != nil {
			continue
		}

		metadata := storage.RuntimeContainerMetadata{}
		if err := json.Unmarshal([]byte(metadataString), &metadata); err != nil {
			continue
		}
		if !storage.IsCrioContainer(&metadata) {
			continue
		}
		crioContainers = append(crioContainers, id)
		crioImages = append(crioImages, containers[i].ImageID)
	}
	return crioContainers, crioImages, nil
}

func (c ContainerStore) deleteContainer(id string) {
	logrus.Infof("wiping containers")
	if mounted, err := c.store.Unmount(id, true); err != nil || mounted {
		logrus.Errorf("unable to unmount container %s: %v", id, err)
		return
	}
	if err := c.store.DeleteContainer(id); err != nil {
		logrus.Errorf("unable to delete container %s: %v", id, err)
		return
	}
	logrus.Infof("deleted container %s", id)
}

func (c ContainerStore) deleteImage(id string) {
	logrus.Infof("wiping images")
	if _, err := c.store.DeleteImage(id, true); err != nil {
		logrus.Errorf("unable to delete image %s: %v", id, err)
		return
	}
	logrus.Infof("deleted image %s", id)
}
