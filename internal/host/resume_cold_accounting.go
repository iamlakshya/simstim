package host

func (p *bridgeResumePreparationV1) finishColdPreparationV1() error {
	if p.vm != nil {
		release, err := p.vm.lockProducersV1()
		if err != nil {
			return err
		}
		defer release()
		if err := p.vm.verifyBeforePlacementV1(p.lifetime); err != nil {
			return err
		}
	}
	var err error
	p.publication, err = beginRuntimePublicationKind(p.lifetime, "resume_prepare", nil, p.operation)
	if err != nil {
		return err
	}
	if err := admitRestartMemoryRequestV1(p.lifetime, p.operation, p.args); err != nil {
		return err
	}
	preview, err := p.operation.LoadContext(p.lifetime)
	if err != nil {
		return err
	}
	if err := ApplyResumeLayers(preview, p.args.Layers); err != nil {
		return err
	}
	if err := ApplyRepositoryResume(preview, p.args); err != nil {
		return err
	}
	if err := applyAdmittedRestartMemoryV1(p.operation, preview); err != nil {
		return err
	}
	if err := p.cold.CheckManifest(p.lifetime, p.operation, preview); err != nil {
		return err
	}
	if err := activateManagerCustodyPreparationV1(p.lifetime, p.operation, preview); err != nil {
		return err
	}
	p.accounting, err = beginResumePreparationV1(p.lifetime, p.operation, p.cold, preview, p.token)
	return err
}
